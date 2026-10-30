// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/mqtt"
	"github.com/otfabric/go-openadr/notification/webhook"
	"github.com/otfabric/go-openadr/notification/websocket"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// The tests in this file put a VTN and its clients under load and under
// failure: many clients at once, a store that fails, a network that cuts
// connections, webhooks that do not answer, a broker that drops everyone,
// tokens that expire, a process that dies mid-write. None of them asks
// that nothing goes wrong. They ask that what goes wrong is reported as
// what it is, that nothing is corrupted or leaked by it, and that
// everything works again when it is over.
//
// What happens is random and follows a seed, which a failing test prints:
// OPENADR_CHAOS_SEED=<seed> runs it again the same way, as far as the
// scheduler allows.

// chaosSeed returns the seed of a test.
func chaosSeed(t *testing.T) uint64 {
	t.Helper()
	seed := uint64(time.Now().UnixNano())
	if v := os.Getenv("OPENADR_CHAOS_SEED"); v != "" {
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("OPENADR_CHAOS_SEED: %v", err)
		}
		seed = parsed
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("to run this again: OPENADR_CHAOS_SEED=%d", seed)
		}
	})
	return seed
}

// rounds scales a number of operations down for go test -short.
func rounds(n int) int {
	if testing.Short() {
		return max(n/5, 1)
	}
	return n
}

// failures collects what went wrong in goroutines, where t.Fatal may not
// be called, and keeps the report short.
type failures struct {
	t     *testing.T
	count atomic.Int64
}

func (f *failures) add(format string, args ...any) {
	if f.count.Add(1) <= 10 {
		f.t.Errorf(format, args...)
	}
}

// status returns the HTTP status an error of the API carries, or 0.
func status(err error) int {
	var apiErr *openadr.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// raced reports whether an error is what a client gets when another
// client was faster: the object it meant is gone, or the name is taken.
func raced(err error) bool {
	return errors.Is(err, openadr.ErrNotFound) || errors.Is(err, openadr.ErrConflict)
}

// consistent checks what a store must hold at any moment: every reference
// leads somewhere, and names are unique where they have to be.
func consistent(t *testing.T, store storage.Store) {
	t.Helper()
	ctx := context.Background()
	programs, _ := store.Programs().List(ctx, storage.ProgramFilter{})
	events, _ := store.Events().List(ctx, storage.EventFilter{})
	vens, _ := store.VENs().List(ctx, storage.VENFilter{})
	resources, _ := store.Resources().List(ctx, storage.ResourceFilter{})
	groups, _ := store.ResourceGroups().List(ctx, storage.ResourceGroupFilter{})
	subscriptions, _ := store.Subscriptions().List(ctx, storage.SubscriptionFilter{})

	ids := map[string]string{}
	seen := func(kind, id string) {
		if other, dup := ids[id]; dup {
			t.Errorf("the ID %s is that of a %s and of a %s", id, other, kind)
		}
		ids[id] = kind
	}
	names := map[string]bool{}
	unique := func(kind, name string) {
		if names[kind+"/"+name] {
			t.Errorf("two %ss are named %q", kind, name)
		}
		names[kind+"/"+name] = true
	}
	for _, p := range programs {
		seen("program", p.ID)
		unique("program", p.Name)
	}
	for _, v := range vens {
		seen("ven", v.ID)
		unique("ven", v.Name)
	}
	for _, e := range events {
		seen("event", e.ID)
		if ids[e.ProgramID] != "program" {
			t.Errorf("event %s belongs to the program %s, which does not exist", e.ID, e.ProgramID)
		}
	}
	for _, r := range resources {
		seen("resource", r.ID)
		unique("resource of "+r.VENID, r.Name)
		if ids[r.VENID] != "ven" {
			t.Errorf("resource %s belongs to the ven %s, which does not exist", r.ID, r.VENID)
		}
	}
	for _, g := range groups {
		seen("group", g.ID)
	}
	for _, g := range groups {
		for _, c := range g.Children {
			want := map[openadr.ResourceGroupChildType]string{openadr.ChildResource: "resource", openadr.ChildResourceGroup: "group"}[c.Type]
			if ids[c.ID] != want {
				t.Errorf("group %s holds the %s %s, which does not exist", g.ID, c.Type, c.ID)
			}
		}
	}
	for _, s := range subscriptions {
		seen("subscription", s.ID)
		if s.ProgramID != "" && ids[s.ProgramID] != "program" {
			t.Errorf("subscription %s is to the program %s, which does not exist", s.ID, s.ProgramID)
		}
	}
}

// --- load

// workload is what the clients of a VTN do all at once.
type workload struct {
	vtn  *stack
	fail *failures
	// tolerate says an error is expected under the failure a test causes.
	tolerate func(error) bool

	// What the clients were told succeeded.
	mu       sync.Mutex
	created  map[string]bool // programs, by ID
	deleted  map[string]bool
	statuses map[int]int
}

func newWorkload(t *testing.T, vtn *stack) *workload {
	return &workload{vtn: vtn, fail: &failures{t: t}, tolerate: func(error) bool { return false },
		created: map[string]bool{}, deleted: map[string]bool{}, statuses: map[int]int{}}
}

// ok reports whether a call succeeded, and complains about an error that
// neither a race with another client nor the test's own failure explains.
func (w *workload) ok(what string, err error) bool {
	if err == nil {
		return true
	}
	w.mu.Lock()
	w.statuses[status(err)]++
	w.mu.Unlock()
	if !raced(err) && !w.tolerate(err) {
		w.fail.add("%s: %v", what, err)
	}
	return false
}

// businessLogic creates, changes and deletes programs, events and groups.
func (w *workload) businessLogic(worker, n int, seed uint64) {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(seed, uint64(worker)))
	bl := w.vtn.BL
	targets := [][]string{nil, {"group-a"}, {"group-b"}, {"group-a", "group-b"}}
	var programs []openadr.Program
	var events []openadr.Event
	for i := range n {
		switch op := rng.IntN(10); {
		case op < 3 || len(programs) == 0:
			p, err := bl.CreateProgram(ctx, openadr.Program{Name: fmt.Sprintf("program-%d-%d", worker, i), Targets: targets[rng.IntN(len(targets))]})
			if w.ok("creating a program", err) {
				programs = append(programs, p)
				w.mu.Lock()
				w.created[p.ID] = true
				w.mu.Unlock()
			}
		case op < 6:
			p := programs[rng.IntN(len(programs))]
			e, err := bl.CreateEvent(ctx, openadr.Event{ProgramID: p.ID, Name: fmt.Sprintf("event-%d-%d", worker, i), Targets: p.Targets})
			if w.ok("creating an event", err) {
				events = append(events, e)
			}
		case op < 7:
			k := rng.IntN(len(programs))
			programs[k].Descriptions = append(programs[k].Descriptions, openadr.ProgramDescription{URL: "https://example.com/" + strconv.Itoa(i)})
			if p, err := bl.UpdateProgram(ctx, programs[k]); w.ok("updating a program", err) {
				programs[k] = p
			}
		case op < 8 && len(events) > 0:
			k := rng.IntN(len(events))
			if _, err := bl.DeleteEvent(ctx, events[k].ID); w.ok("deleting an event", err) || errors.Is(err, openadr.ErrNotFound) {
				events = slices.Delete(events, k, k+1)
			}
		case op < 9:
			// A program with events is not deleted: 409, by the rules.
			k := rng.IntN(len(programs))
			if _, err := bl.DeleteProgram(ctx, programs[k].ID); w.ok("deleting a program", err) {
				w.mu.Lock()
				w.deleted[programs[k].ID] = true
				w.mu.Unlock()
				programs = slices.Delete(programs, k, k+1)
			}
		default:
			resources, err := bl.Resources(ctx, client.ResourceQuery{})
			if !w.ok("searching resources", err) {
				continue
			}
			g := openadr.ResourceGroup{Name: fmt.Sprintf("group-%d-%d", worker, i)}
			for _, r := range resources {
				if rng.IntN(2) == 0 {
					g.Children = append(g.Children, openadr.ResourceGroupChild{Type: openadr.ChildResource, ID: r.ID})
				}
			}
			// A resource may be deleted before the group is made: 409.
			if g, err = bl.CreateResourceGroup(ctx, g); w.ok("creating a resource group", err) && rng.IntN(2) == 0 {
				_, err = bl.DeleteResourceGroup(ctx, g.ID)
				w.ok("deleting a resource group", err)
			}
		}
	}
}

// ven reads what it may, checks that it is shown nothing else, and writes
// reports, resources and subscriptions of its own.
func (w *workload) ven(worker int, id string, ven *client.VEN, granted string, n int, seed uint64) {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(seed, uint64(1000+worker)))
	var venID string
	if list, err := ven.VENs(ctx, client.VENQuery{}); w.ok("reading the own ven", err) && len(list) == 1 {
		venID = list[0].ID
	}
	for i := range n {
		switch op := rng.IntN(10); {
		case op < 3:
			query := client.ProgramQuery{}
			if rng.IntN(2) == 0 {
				query.Targets = []string{"group-a", "group-b"}
			}
			programs, err := ven.Programs(ctx, query)
			if !w.ok("searching programs", err) {
				continue
			}
			for _, p := range programs {
				// Never a target it was not granted, and none at all when
				// it named none.
				if len(p.Targets) > 1 || len(p.Targets) == 1 && (p.Targets[0] != granted || query.Targets == nil) {
					w.fail.add("%s was shown the program %s with the targets %v (asked for %v)", id, p.Name, p.Targets, query.Targets)
				}
			}
		case op < 6:
			events, err := ven.Events(ctx, client.EventQuery{Targets: []string{granted}})
			if !w.ok("searching events", err) || len(events) == 0 {
				continue
			}
			e := events[rng.IntN(len(events))]
			if len(e.Targets) != 1 || e.Targets[0] != granted {
				w.fail.add("%s was shown the event %s with the targets %v", id, e.Name, e.Targets)
			}
			// The event may be deleted meanwhile: 409.
			_, err = ven.CreateReport(ctx, openadr.Report{EventID: e.ID, ClientName: fmt.Sprintf("%s-%d-%d", id, worker, i), Resources: []openadr.ReportResource{}})
			w.ok("reporting", err)
		case op < 7:
			reports, err := ven.Reports(ctx, client.ReportQuery{})
			if !w.ok("searching reports", err) {
				continue
			}
			for _, r := range reports {
				if r.ClientID != id {
					w.fail.add("%s was shown a report of %s", id, r.ClientID)
				}
			}
		case op < 9 && venID != "":
			res, err := ven.CreateResource(ctx, openadr.Resource{VENID: venID, Name: fmt.Sprintf("resource-%d-%d", worker, i)})
			if w.ok("creating a resource", err) && rng.IntN(3) > 0 {
				_, err = ven.DeleteResource(ctx, res.ID)
				w.ok("deleting a resource", err)
			}
		default:
			sub, err := ven.CreateSubscription(ctx, openadr.Subscription{ClientName: id, ObjectOperations: bySocket(openadr.ObjectEvent), Targets: []string{granted}})
			if w.ok("subscribing", err) {
				groups, err := ven.ResourceGroups(ctx, client.ResourceGroupQuery{})
				w.ok("searching resource groups", err)
				for _, g := range groups {
					if len(g.Children) == 0 {
						w.fail.add("%s was shown the group %s, which holds nothing of its own", id, g.Name)
					}
				}
				_, err = ven.DeleteSubscription(ctx, sub.ID)
				w.ok("unsubscribing", err)
			}
		}
	}
}

// setup gives each VEN client a ven, with a target: once, and before
// anything is made to fail.
func (w *workload) setup(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := w.vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-1", ClientID: "ven-1", Targets: []string{"group-a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-2", ClientID: "ven-2", Targets: []string{"group-b"}}); err != nil {
		t.Fatal(err)
	}
}

// run starts the clients and waits for them.
func (w *workload) run(seed uint64, perClient int) {
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(3)
		go func() { defer wg.Done(); w.businessLogic(i, perClient, seed) }()
		go func() { defer wg.Done(); w.ven(i, "ven-1", w.vtn.VEN1, "group-a", perClient, seed) }()
		go func() { defer wg.Done(); w.ven(4+i, "ven-2", w.vtn.VEN2, "group-b", perClient, seed) }()
	}
	wg.Wait()
}

// Twelve clients at once, on every kind of object, on both stores. Run
// with the race detector, this is the test of the VTN's locking; without
// it, of its rules holding when clients get in each other's way.
func TestParallelClients(t *testing.T) {
	for _, kind := range []string{"memory", "journal"} {
		t.Run(kind, func(t *testing.T) {
			seed := chaosSeed(t)
			var store storage.Store = storage.NewMemory()
			path := filepath.Join(t.TempDir(), "vtn.journal")
			if kind == "journal" {
				file, err := storage.OpenFile(path, storage.FileOptions{NoSync: true})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				store = file
			}
			vtn := start(t, options{store: store, mqtt: true, groups: true, websocket: &server.WebSocketConfig{Queue: 1 << 16}})

			// Business Logic listens to everything that happens to programs.
			if _, err := vtn.BL.CreateSubscription(context.Background(), openadr.Subscription{ClientName: "bl", ObjectOperations: bySocket(openadr.ObjectProgram)}); err != nil {
				t.Fatal(err)
			}
			var (
				mu      sync.Mutex
				history = map[string][]openadr.Operation{}
				told    atomic.Int64
			)
			sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: vtn.BL.NotificationStream,
				Notify: func(_ context.Context, n openadr.Notification) {
					p := n.Object.(openadr.Program)
					mu.Lock()
					history[p.ID] = append(history[p.ID], n.Operation)
					mu.Unlock()
					told.Add(1)
				}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sub.Close() }()
			if err := sub.Ready(context.Background()); err != nil {
				t.Fatal(err)
			}

			w := newWorkload(t, vtn)
			w.setup(t)
			w.run(seed, rounds(150))
			consistent(t, vtn.Store)
			if n := w.statuses[0] + w.statuses[500]; n > 0 {
				t.Errorf("%d calls failed for no reason of the API: %v", n, w.statuses)
			}

			// It was told of every program, in the order things happened
			// to it: created, changed, and for some deleted.
			eventually(t, "Business Logic was told of every program", func() bool {
				mu.Lock()
				defer mu.Unlock()
				for id := range w.created {
					ops := history[id]
					if len(ops) == 0 || w.deleted[id] && ops[len(ops)-1] != openadr.OperationDelete {
						return false
					}
				}
				return true
			})
			mu.Lock()
			for id, ops := range history {
				for i, op := range ops {
					first, last := i == 0, i == len(ops)-1
					if (op == openadr.OperationCreate) != first || op == openadr.OperationDelete && !last {
						t.Errorf("program %s: notified out of order: %v", id, ops)
						break
					}
				}
				if !w.created[id] {
					t.Errorf("a notification of the program %s, which no client was told it created", id)
				}
			}
			mu.Unlock()
			if sub.Sessions() != 1 {
				t.Errorf("the WebSocket was opened %d times", sub.Sessions())
			}
			t.Logf("%d programs created, %d deleted, %d notifications, statuses of refused calls: %v", len(w.created), len(w.deleted), told.Load(), w.statuses)

			if kind != "journal" {
				return
			}
			// What the journal holds is what the VTN held.
			before, _ := vtn.Store.Programs().List(context.Background(), storage.ProgramFilter{})
			if err := store.(*storage.File).Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := storage.OpenFile(path, storage.FileOptions{})
			if err != nil {
				t.Fatalf("the journal of a busy VTN does not open: %v", err)
			}
			defer func() { _ = reopened.Close() }()
			consistent(t, reopened)
			after, _ := reopened.Programs().List(context.Background(), storage.ProgramFilter{})
			if len(after) != len(before) {
				t.Errorf("%d programs before the restart, %d after", len(before), len(after))
			}
		})
	}
}

// --- a store that fails

// faultyStore fails some of the calls to a store: before the call, so that
// nothing happens, or after it, so that it happened and nobody is told.
type faultyStore struct {
	storage.Store
	on   atomic.Bool
	mu   sync.Mutex
	rng  *rand.Rand
	hits atomic.Int64
}

var errDiskOnFire = errors.New("disk on fire: /var/lib/vtn/secret-path")

// fault says whether this call fails, and whether before or after.
func (s *faultyStore) fault() (before, after bool) {
	if !s.on.Load() {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.rng.IntN(10) {
	case 0:
		s.hits.Add(1)
		return true, false
	case 1:
		s.hits.Add(1)
		return false, true
	}
	return false, false
}

type faultyCollection[T, F any] struct {
	storage.Collection[T, F]
	s *faultyStore
}

func (c faultyCollection[T, F]) Create(ctx context.Context, v T) error {
	before, after := c.s.fault()
	if before {
		return errDiskOnFire
	}
	if err := c.Collection.Create(ctx, v); err != nil || !after {
		return err
	}
	return errDiskOnFire
}

func (c faultyCollection[T, F]) Update(ctx context.Context, v T) error {
	before, after := c.s.fault()
	if before {
		return errDiskOnFire
	}
	if err := c.Collection.Update(ctx, v); err != nil || !after {
		return err
	}
	return errDiskOnFire
}

func (c faultyCollection[T, F]) Delete(ctx context.Context, id string) (T, error) {
	before, after := c.s.fault()
	if before {
		var zero T
		return zero, errDiskOnFire
	}
	v, err := c.Collection.Delete(ctx, id)
	if err != nil || !after {
		return v, err
	}
	return v, errDiskOnFire
}

func (c faultyCollection[T, F]) Get(ctx context.Context, id string) (T, error) {
	if before, _ := c.s.fault(); before {
		var zero T
		return zero, errDiskOnFire
	}
	return c.Collection.Get(ctx, id)
}

func (c faultyCollection[T, F]) List(ctx context.Context, f F) ([]T, error) {
	if before, _ := c.s.fault(); before {
		return nil, errDiskOnFire
	}
	return c.Collection.List(ctx, f)
}

func (s *faultyStore) Programs() storage.Collection[openadr.Program, storage.ProgramFilter] {
	return faultyCollection[openadr.Program, storage.ProgramFilter]{s.Store.Programs(), s}
}

func (s *faultyStore) Events() storage.Collection[openadr.Event, storage.EventFilter] {
	return faultyCollection[openadr.Event, storage.EventFilter]{s.Store.Events(), s}
}

func (s *faultyStore) Reports() storage.Collection[openadr.Report, storage.ReportFilter] {
	return faultyCollection[openadr.Report, storage.ReportFilter]{s.Store.Reports(), s}
}

func (s *faultyStore) VENs() storage.Collection[openadr.VEN, storage.VENFilter] {
	return faultyCollection[openadr.VEN, storage.VENFilter]{s.Store.VENs(), s}
}

func (s *faultyStore) Resources() storage.Collection[openadr.Resource, storage.ResourceFilter] {
	return faultyCollection[openadr.Resource, storage.ResourceFilter]{s.Store.Resources(), s}
}

func (s *faultyStore) ResourceGroups() storage.Collection[openadr.ResourceGroup, storage.ResourceGroupFilter] {
	return faultyCollection[openadr.ResourceGroup, storage.ResourceGroupFilter]{s.Store.ResourceGroups(), s}
}

func (s *faultyStore) Subscriptions() storage.Collection[storage.SubscriptionRecord, storage.SubscriptionFilter] {
	return faultyCollection[storage.SubscriptionRecord, storage.SubscriptionFilter]{s.Store.Subscriptions(), s}
}

// One call in five to the store fails. Clients are told 500 and nothing of
// why; nobody is notified of what a client was not told happened; nothing
// is left half done; and when the store is well again, so is the VTN.
func TestChaosStore(t *testing.T) {
	seed := chaosSeed(t)
	inner := storage.NewMemory()
	faulty := &faultyStore{Store: inner, rng: rand.New(rand.NewPCG(seed, 99))}
	vtn := start(t, options{store: faulty, groups: true, websocket: &server.WebSocketConfig{Queue: 1 << 16}})
	ctx := context.Background()

	if _, err := vtn.BL.CreateSubscription(ctx, openadr.Subscription{ClientName: "bl", ObjectOperations: bySocket(openadr.ObjectProgram)}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	notified := map[string]bool{}
	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: vtn.BL.NotificationStream,
		Notify: func(_ context.Context, n openadr.Notification) {
			if n.Operation == openadr.OperationCreate {
				mu.Lock()
				notified[n.Object.(openadr.Program).ID] = true
				mu.Unlock()
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}

	w := newWorkload(t, vtn)
	var leaked atomic.Int64
	w.tolerate = func(err error) bool {
		var apiErr *openadr.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
			return false
		}
		// The client is not told what went wrong inside.
		if strings.Contains(apiErr.Error(), "fire") || strings.Contains(apiErr.Problem.Detail, "secret") {
			leaked.Add(1)
		}
		return true
	}
	w.setup(t)
	faulty.on.Store(true)
	w.run(seed, rounds(120))
	faulty.on.Store(false)

	if faulty.hits.Load() == 0 || w.statuses[500] == 0 {
		t.Fatalf("no call failed: %d faults, statuses %v", faulty.hits.Load(), w.statuses)
	}
	if leaked.Load() > 0 {
		t.Errorf("%d answers told the client what went wrong in the store", leaked.Load())
	}
	if w.statuses[0] > 0 {
		t.Errorf("%d calls failed below the API: %v", w.statuses[0], w.statuses)
	}
	consistent(t, inner)

	// Nobody is notified of what a client was told had failed: not of a
	// program that was never made, and not of one that was made and whose
	// maker was told 500. The other way round does not hold: to notify,
	// the VTN reads its subscriptions from the same store, and when that
	// fails, the notification is lost. A notification is a hint.
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	for id := range notified {
		if !w.created[id] {
			t.Errorf("a notification of the program %s, whose creation was answered with an error", id)
		}
	}
	missed := len(w.created) - len(notified)
	mu.Unlock()

	// And everything works again, notifications too.
	p := mustProgram(t, vtn.BL, openadr.Program{Name: "after the fire"})
	if _, err := vtn.VEN1.Program(ctx, p.ID); err != nil {
		t.Errorf("after the faults: %v", err)
	}
	eventually(t, "a program made after the faults was notified of", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return notified[p.ID]
	})
	t.Logf("%d of %d notifications were lost to the store", missed, len(w.created))
	t.Logf("%d faults; statuses of refused calls: %v", faulty.hits.Load(), w.statuses)
}

// --- a network that fails

// flakyProxy forwards TCP connections to a VTN and, while it is on, cuts
// some of them off after some bytes, in whichever direction gets there.
type flakyProxy struct {
	listener net.Listener
	target   string
	on       atomic.Bool
	cuts     atomic.Int64

	mu     sync.Mutex
	rng    *rand.Rand
	conns  map[net.Conn]bool
	closed bool
	wg     sync.WaitGroup
}

// cutAll cuts every connection there is, the quiet ones too.
func (p *flakyProxy) cutAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for conn := range p.conns {
		_ = conn.Close()
	}
	p.cuts.Add(int64(len(p.conns) / 2))
}

func newFlakyProxy(t *testing.T, target string, seed uint64) *flakyProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &flakyProxy{listener: ln, target: target, rng: rand.New(rand.NewPCG(seed, 7)), conns: map[net.Conn]bool{}}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go p.serve(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		p.on.Store(false)
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.cutAll()
		p.wg.Wait()
	})
	return p
}

func (p *flakyProxy) serve(client net.Conn) {
	defer p.wg.Done()
	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	// How many bytes this connection carries before it is cut; -1 for one
	// that is left alone.
	budget := int64(-1)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	if p.on.Load() && p.rng.IntN(3) == 0 {
		budget = p.rng.Int64N(6000)
	}
	p.conns[client], p.conns[upstream] = true, true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.conns, client)
		delete(p.conns, upstream)
		p.mu.Unlock()
	}()
	var left atomic.Int64
	left.Store(budget)
	both := func() {
		_ = client.Close()
		_ = upstream.Close()
	}
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 2048)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				if budget >= 0 && left.Add(-int64(n)) < 0 {
					p.cuts.Add(1)
					both()
					return
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					both()
					return
				}
			}
			if err != nil {
				both()
				return
			}
		}
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		pipe(upstream, client)
	}()
	pipe(client, upstream)
}

// url is the address of the VTN through the proxy.
func (p *flakyProxy) url(vtn *stack) string {
	return "https://" + p.listener.Addr().String() + strings.TrimPrefix(vtn.URL, vtn.Server.URL)
}

// A third of the connections between a client and the VTN are cut, in the
// middle of a request or of its answer. The client is told that the call
// failed and not what became of it; it tries again, and finds out. In the
// end everything it meant to create exists, once.
func TestChaosNetwork(t *testing.T) {
	seed := chaosSeed(t)
	vtn := start(t, options{websocket: &server.WebSocketConfig{}})
	proxy := newFlakyProxy(t, strings.TrimPrefix(vtn.Server.URL, "https://"), seed)
	ctx := context.Background()

	// Clients that reach the VTN through the proxy, each with connections
	// of its own. The token endpoint is the VTN's, announced by its direct
	// address: cut that too, by naming it.
	through := func(id string) client.Config {
		transport := vtn.HTTP.Transport.(*http.Transport).Clone()
		return client.Config{BaseURL: proxy.url(vtn), HTTPClient: &http.Client{Transport: transport, Timeout: 5 * time.Second},
			Credentials: &client.Credentials{ClientID: id, ClientSecret: id + "-secret", TokenURL: proxy.url(vtn) + "/auth/token"}}
	}
	bl, err := client.NewBL(through("bl"))
	if err != nil {
		t.Fatal(err)
	}
	ven, err := client.NewVEN(through("ven-1"))
	if err != nil {
		t.Fatal(err)
	}

	// A listener through the proxy too: its WebSocket is cut like the rest.
	if _, err := ven.CreateSubscription(ctx, openadr.Subscription{ClientName: "ven-1", ObjectOperations: bySocket(openadr.ObjectProgram)}); err != nil {
		t.Fatal(err)
	}
	received := make(chan openadr.Notification, 4096)
	sub, err := websocket.NewSubscriber(websocket.SubscriberConfig{Dial: ven.NotificationStream,
		Notify:    func(_ context.Context, n openadr.Notification) { received <- n },
		KeepAlive: 50 * time.Millisecond, Backoff: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Ready(ctx); err != nil {
		t.Fatal(err)
	}

	proxy.on.Store(true)
	const writers = 6
	perWriter := rounds(40)
	fail := &failures{t: t}
	var (
		wg                 sync.WaitGroup
		lost, tries, meant atomic.Int64
		stormOver          atomic.Bool
	)
	// Meanwhile, now and then, everything goes at once: the connections
	// that were opened in better times, the WebSocket among them. The
	// writers go on until that is over, however fast the machine is.
	storm := make(chan struct{})
	go func() {
		defer close(storm)
		for range 5 {
			time.Sleep(40 * time.Millisecond)
			proxy.cutAll()
		}
		stormOver.Store(true)
	}()
	for wkr := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter || !stormOver.Load(); i++ {
				meant.Add(1)
				name := fmt.Sprintf("program-%d-%d", wkr, i)
				for attempt := 0; ; attempt++ {
					tries.Add(1)
					_, err := bl.CreateProgram(ctx, openadr.Program{Name: name})
					if err == nil {
						break
					}
					if attempt > 0 && errors.Is(err, openadr.ErrConflict) {
						// The answer to an earlier attempt was lost; the
						// program was not.
						lost.Add(1)
						break
					}
					if status(err) != 0 || attempt > 50 {
						fail.add("creating %s, attempt %d: %v", name, attempt, err)
						break
					}
				}
				// Reads fail the same way, and are simply read again.
				for attempt := 0; attempt < 50; attempt++ {
					if _, err := ven.Programs(ctx, client.ProgramQuery{Page: client.Page{Limit: 5}}); err == nil {
						break
					} else if status(err) != 0 {
						fail.add("reading: %v", err)
						break
					}
				}
			}
		}()
	}
	wg.Wait()
	<-storm
	proxy.on.Store(false)
	if proxy.cuts.Load() == 0 {
		t.Fatal("no connection was cut")
	}

	// Everything exists, once.
	consistent(t, vtn.Store)
	all, _ := vtn.Store.Programs().List(ctx, storage.ProgramFilter{})
	if int64(len(all)) != meant.Load() {
		t.Errorf("%d programs exist, %d were meant to", len(all), meant.Load())
	}

	// The listener was cut off, came back, and hears again.
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sub.Ready(readyCtx); err != nil {
		t.Fatalf("the WebSocket did not come back: %v", err)
	}
	if sub.Sessions() < 2 {
		t.Errorf("the WebSocket was never cut: %d sessions", sub.Sessions())
	}
	for len(received) > 0 {
		<-received
	}
	// Ready may still see the session of the last cut, and what is sent
	// while the listener is away is missed: it hears one of these.
	heard := 0
	eventually(t, "the listener heard of a program after the storm", func() bool {
		heard++
		last := mustProgram(t, vtn.BL, openadr.Program{Name: fmt.Sprintf("after the storm %d", heard)})
		timeout := time.After(200 * time.Millisecond)
		for {
			select {
			case n := <-received:
				if p, ok := n.Object.(openadr.Program); ok && p.ID == last.ID {
					return true
				}
			case <-timeout:
				return false
			}
		}
	})
	t.Logf("%d connections cut; %d calls for %d programs, %d of them created without the client hearing of it; %d WebSocket sessions",
		proxy.cuts.Load(), tries.Load(), meant.Load(), lost.Load(), sub.Sessions())
}

// --- webhooks that fail

// A webhook that fails most of the time, in every way an endpoint can: it
// answers 500, it hangs, it hangs up. The VTN keeps trying, in order, and
// its clients do not wait for it.
func TestChaosWebhooks(t *testing.T) {
	seed := chaosSeed(t)
	vtn := start(t, options{sender: &webhook.SenderConfig{Attempts: 200, Backoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond, Timeout: 100 * time.Millisecond}})
	ctx := context.Background()

	var (
		mu       sync.Mutex
		rng      = rand.New(rand.NewPCG(seed, 3))
		calm     atomic.Bool
		accepted []string
		faults   atomic.Int64
	)
	calm.Store(true) // until the subscription is verified
	handler := &webhook.Handler{Notify: func(_ context.Context, n openadr.Notification) error {
		mu.Lock()
		defer mu.Unlock()
		accepted = append(accepted, n.Object.(openadr.Program).Name)
		return nil
	}}
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		mood := rng.IntN(6)
		mu.Unlock()
		if calm.Load() || r.Method != http.MethodPost {
			mood = 0
		}
		switch mood {
		case 1, 2:
			faults.Add(1)
			http.Error(w, "not now", http.StatusInternalServerError)
		case 3:
			faults.Add(1)
			// Longer than the VTN waits.
			select {
			case <-r.Context().Done():
			case <-time.After(300 * time.Millisecond):
			}
		case 4:
			faults.Add(1)
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = conn.Close()
			}
		case 5:
			// Taken, and the answer lost: the VTN will send it again.
			faults.Add(1)
			handler.ServeHTTP(httptest.NewRecorder(), r)
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = conn.Close()
			}
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	defer endpoint.Close()

	sub := openadr.Subscription{ClientName: "bl", ObjectOperations: all(openadr.ObjectProgram)}
	sub.ObjectOperations[0].Operations = []openadr.Operation{openadr.OperationCreate}
	sub.ObjectOperations[0].CallbackURL = endpoint.URL
	if _, err := vtn.BL.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	calm.Store(false)

	n := rounds(60)
	var want []string
	var slowest time.Duration
	for i := range n {
		name := fmt.Sprintf("program-%03d", i)
		want = append(want, name)
		start := time.Now()
		mustProgram(t, vtn.BL, openadr.Program{Name: name})
		slowest = max(slowest, time.Since(start))
	}
	// The API does not wait for the webhook, which takes 100 ms to give up
	// on one attempt alone.
	if slowest > 2*time.Second {
		t.Errorf("a call took %v while the webhook was failing", slowest)
	}

	// Every notification arrives, and the first arrival of each is in the
	// order the programs were made. Some arrive twice.
	distinct := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, name := range accepted {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		return out
	}
	deadline := time.Now().Add(60 * time.Second)
	for len(distinct()) < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := distinct()
	if !slices.Equal(got, want) {
		t.Errorf("delivered %d of %d, or out of order:\n got %v\nwant %v", len(got), n, got, want)
	}
	mu.Lock()
	duplicates := len(accepted) - len(got)
	mu.Unlock()
	if faults.Load() == 0 {
		t.Fatal("the webhook never failed")
	}
	t.Logf("%d notifications through %d failures of the webhook, %d delivered twice; slowest call %v", n, faults.Load(), duplicates, slowest)
}

// --- a broker that fails

// The broker drops every connection, again and again, while the VTN
// publishes. The VTN's calls do not wait for it, and when it is over the
// publisher and the subscriber are both back.
func TestChaosBroker(t *testing.T) {
	vtn := start(t, options{mqtt: true})
	ctx := context.Background()
	topics, err := vtn.BL.MQTTTopics(ctx, client.AllPrograms())
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan openadr.Notification, 4096)
	listener, err := mqtt.NewSubscriber(mqtt.SubscriberConfig{
		Broker: mqtt.Broker{URL: vtn.Broker.URL(), ClientID: "bl", Username: "client", Password: "client-broker-password", Backoff: 5 * time.Millisecond},
		Topics: []string{topics.All},
		Notify: func(_ context.Context, _ string, n openadr.Notification) { received <- n },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err := listener.Ready(ctx); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var drops atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(15 * time.Millisecond):
				vtn.Broker.DropClients()
				drops.Add(1)
			}
		}
	}()
	// Publishing goes on until the broker has dropped everyone a few
	// times, however fast the machine is: a fixed number of programs is
	// made in less than the time to the first drop on a fast one.
	var slowest time.Duration
	deadline := time.Now().Add(30 * time.Second)
	for i := 0; i < rounds(100) || drops.Load() < 3; i++ {
		if time.Now().After(deadline) {
			t.Fatalf("the broker dropped its clients %d times in 30 s", drops.Load())
		}
		start := time.Now()
		mustProgram(t, vtn.BL, openadr.Program{Name: fmt.Sprintf("program-%04d", i)})
		slowest = max(slowest, time.Since(start))
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if slowest > 2*time.Second {
		t.Errorf("a call took %v while the broker was failing", slowest)
	}

	// Back: what is published now arrives. (What was published during the
	// outages may or may not have: MQTT with clean sessions keeps nothing
	// for a client that is away.)
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := listener.Ready(readyCtx); err != nil {
		t.Fatalf("the subscriber did not come back: %v", err)
	}
	if listener.Sessions() < 2 {
		t.Errorf("the subscriber was never dropped: %d sessions after %d drops", listener.Sessions(), drops.Load())
	}
	deadline = time.Now().Add(10 * time.Second)
	for i := 0; ; i++ {
		last := mustProgram(t, vtn.BL, openadr.Program{Name: fmt.Sprintf("after-the-outage-%d", i)})
		arrived := false
		wait := time.After(500 * time.Millisecond)
	drain:
		for {
			select {
			case n := <-received:
				if p, ok := n.Object.(openadr.Program); ok && p.ID == last.ID {
					arrived = true
					break drain
				}
			case <-wait:
				break drain
			}
		}
		if arrived {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nothing published after the outages arrived")
		}
	}
	t.Logf("%d drops, %d sessions of the subscriber, slowest call %v", drops.Load(), listener.Sessions(), slowest)
}

// --- tokens that expire

// Tokens that live for a moment, held by clients that do not look at the
// clock: every call that finds its token expired gets a new one and goes
// through, however many clients find out at once.
func TestChaosTokens(t *testing.T) {
	vtn := start(t, options{jwt: true})
	ctx := context.Background()
	// The expiry of a token is written in whole seconds: one minted for a
	// second and a half lives for half a second at least.
	shortLived := func(id string, scopes []string) *tokenSource {
		return &tokenSource{mint: func() string { return vtn.idp.mint(id, scopes, 1500*time.Millisecond, nil) }}
	}
	blTokens, venTokens := shortLived("bl", auth.BusinessLogicScopes()), shortLived("ven-1", auth.VENScopes())
	bl, err := client.NewBL(client.Config{BaseURL: vtn.URL, HTTPClient: vtn.HTTP, Tokens: blTokens})
	if err != nil {
		t.Fatal(err)
	}
	ven, err := client.NewVEN(client.Config{BaseURL: vtn.URL, HTTPClient: vtn.HTTP, Tokens: venTokens})
	if err != nil {
		t.Fatal(err)
	}
	fail := &failures{t: t}
	var (
		wg    sync.WaitGroup
		calls atomic.Int64
	)
	// Long enough for the tokens to expire a few times.
	until := time.Now().Add(time.Duration(rounds(50)) * 100 * time.Millisecond)
	for wkr := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; time.Now().Before(until); i++ {
				if _, err := bl.CreateProgram(ctx, openadr.Program{Name: fmt.Sprintf("program-%d-%d", wkr, i)}); err != nil {
					fail.add("creating a program: %v", err)
				} else {
					calls.Add(1)
				}
				if _, err := ven.Programs(ctx, client.ProgramQuery{Page: client.Page{Limit: 1}}); err != nil {
					fail.add("reading programs: %v", err)
				}
				time.Sleep(time.Duration(5+wkr) * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	blTokens.mu.Lock()
	minted := blTokens.minted
	blTokens.mu.Unlock()
	if minted < 2 {
		t.Errorf("the token was renewed %d times: it never expired", minted)
	}
	all, _ := vtn.Store.Programs().List(ctx, storage.ProgramFilter{})
	if int64(len(all)) != calls.Load() {
		t.Errorf("%d programs exist, %d were created: a call was repeated or lost", len(all), calls.Load())
	}
	t.Logf("%d tokens for %d calls", minted, calls.Load())
}

// --- a process that dies

// The journal of a busy VTN, cut off at any byte, as a crash leaves it:
// it opens, and holds a state the VTN was once in.
func TestChaosCrash(t *testing.T) {
	seed := chaosSeed(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "vtn.journal")
	file, err := storage.OpenFile(path, storage.FileOptions{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	vtn := start(t, options{store: file, groups: true, websocket: &server.WebSocketConfig{}})
	w := newWorkload(t, vtn)
	w.setup(t)
	w.run(seed, rounds(60))
	journal, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if len(journal) < 1000 {
		t.Fatalf("a journal of %d bytes", len(journal))
	}

	rng := rand.New(rand.NewPCG(seed, 5))
	cuts := []int{0, 1, len(journal) - 1, len(journal)}
	for range rounds(40) {
		cuts = append(cuts, rng.IntN(len(journal)))
	}
	previous := -1
	slices.Sort(cuts)
	for _, cut := range cuts {
		crashed := filepath.Join(dir, fmt.Sprintf("crashed-%d.journal", cut))
		if err := os.WriteFile(crashed, journal[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		recovered, err := storage.OpenFile(crashed, storage.FileOptions{NoSync: true})
		if err != nil {
			t.Fatalf("a journal cut at byte %d of %d does not open: %v", cut, len(journal), err)
		}
		consistent(t, recovered)
		// More of the journal is never less of the VTN's history: the
		// number of changes it replays does not go down.
		lines := strings.Count(string(journal[:cut]), "\n")
		if lines < previous {
			t.Errorf("cut at %d: %d complete records, fewer than a shorter journal had", cut, lines)
		}
		previous = lines
		// And it is a store again: it takes a write, and keeps it.
		if err := recovered.Programs().Create(context.Background(), openadr.Program{Metadata: openadr.Metadata{ID: "after-the-crash"}, Name: "after the crash " + strconv.Itoa(cut)}); err != nil {
			t.Errorf("cut at %d: the recovered store takes no write: %v", cut, err)
		}
		if err := recovered.Close(); err != nil {
			t.Error(err)
		}
		again, err := storage.OpenFile(crashed, storage.FileOptions{NoSync: true})
		if err != nil {
			t.Fatalf("cut at %d: the recovered journal does not open a second time: %v", cut, err)
		}
		if _, err := again.Programs().Get(context.Background(), "after-the-crash"); err != nil {
			t.Errorf("cut at %d: the write after the crash is gone: %v", cut, err)
		}
		_ = again.Close()
	}

	// Garbage in the middle is not a crash, and is not papered over.
	broken := slices.Clone(journal)
	copy(broken[len(broken)/2:], "\n{not json}\n")
	bad := filepath.Join(dir, "broken.journal")
	if err := os.WriteFile(bad, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.OpenFile(bad, storage.FileOptions{}); !errors.Is(err, storage.ErrCorrupt) {
		t.Errorf("a journal with garbage in the middle: %v, want ErrCorrupt", err)
	}
}

// SPDX-License-Identifier: MIT

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/notification/webhook"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// endpoint is a webhook whose mood the test sets: it takes notifications,
// or answers every one with a status.
type endpoint struct {
	*httptest.Server
	// status is what a notification is answered with; 0 takes it.
	status   atomic.Int64
	requests atomic.Int64

	mu    sync.Mutex
	names []string
}

func newEndpoint(t *testing.T) *endpoint {
	t.Helper()
	e := &endpoint{}
	e.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The verification of a new subscription.
			_, _ = io.WriteString(w, r.URL.Query().Get("echo"))
			return
		}
		e.requests.Add(1)
		if status := e.status.Load(); status != 0 {
			w.WriteHeader(int(status))
			return
		}
		var n struct {
			Object struct {
				Name string `json:"programName"`
			} `json:"object"`
		}
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		e.mu.Lock()
		e.names = append(e.names, n.Object.Name)
		e.mu.Unlock()
	}))
	t.Cleanup(e.Close)
	return e
}

func (e *endpoint) got() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.names)
}

// crash stops a VTN as a failing process stops: what its sender still had
// to do is not done.
func (s *stack) crash() {
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.Sender.Close(gone)
	_ = s.VTN.Close()
}

func pending(t *testing.T, store storage.Store) int {
	t.Helper()
	list, err := store.(storage.Outbox).Deliveries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

// A VTN with an outbox does not lose a notification to a webhook that is
// down, nor to its own end: what was decided is delivered, by the VTN
// that decided it or by the one that comes after.
func TestDurableNotifications(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vtn.journal")
	open := func() (*storage.File, *stack) {
		store, err := storage.OpenFile(path, storage.FileOptions{NoSync: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store, start(t, options{store: store, outbox: true, redeliver: 30 * time.Millisecond,
			sender: &webhook.SenderConfig{Attempts: 2, Backoff: 5 * time.Millisecond, Timeout: 500 * time.Millisecond}})
	}
	store, vtn := open()
	hook := newEndpoint(t)
	sub := openadr.Subscription{ClientName: "bl", ObjectOperations: []openadr.ObjectOperation{{
		Objects: []openadr.ObjectType{openadr.ObjectProgram}, Operations: []openadr.Operation{openadr.OperationCreate}, CallbackURL: hook.URL,
	}}}
	created, err := vtn.BL.CreateSubscription(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}

	// While all is well, a notification is delivered and forgotten.
	mustProgram(t, vtn.BL, openadr.Program{Name: "before"})
	eventually(t, "the first notification arrived and left the outbox", func() bool {
		return len(hook.got()) == 1 && pending(t, store) == 0
	})

	// The webhook goes down. Notifications pile up in the outbox, and the
	// sender's attempts for them run out.
	hook.status.Store(http.StatusServiceUnavailable)
	var want []string
	for i := range 5 {
		name := fmt.Sprintf("while-down-%d", i)
		want = append(want, name)
		mustProgram(t, vtn.BL, openadr.Program{Name: name})
	}
	eventually(t, "the webhook was tried", func() bool { return hook.requests.Load() >= 6 })
	if n := pending(t, store); n != 5 {
		t.Fatalf("%d notifications are kept, want 5", n)
	}

	// The VTN dies with them, and another starts on what it left behind,
	// the webhook still down.
	vtn.crash()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, vtn = open()
	if n := pending(t, store); n != 5 {
		t.Fatalf("after the restart %d notifications are kept, want 5", n)
	}
	time.Sleep(100 * time.Millisecond)
	if got := hook.got(); len(got) != 1 {
		t.Fatalf("a webhook that is down took %v", got)
	}

	// The webhook comes back: all five arrive, in the order they were
	// decided, and the outbox is empty.
	hook.status.Store(0)
	eventually(t, "the kept notifications arrived", func() bool { return len(hook.got()) >= 6 && pending(t, store) == 0 })
	got := hook.got()[1:]
	// Each at least once; the first arrival of each in order.
	var first []string
	for _, name := range got {
		if !slices.Contains(first, name) {
			first = append(first, name)
		}
	}
	if !slices.Equal(first, want) {
		t.Errorf("arrived as %v, want %v", got, want)
	}

	// A webhook that refuses for good is not tried again.
	hook.status.Store(http.StatusGone)
	before := hook.requests.Load()
	mustProgram(t, vtn.BL, openadr.Program{Name: "refused"})
	eventually(t, "the refused notification was given up", func() bool { return hook.requests.Load() == before+1 && pending(t, store) == 0 })
	time.Sleep(100 * time.Millisecond)
	if n := hook.requests.Load() - before; n != 1 {
		t.Errorf("a notification the webhook refused for good was sent %d times", n)
	}

	// A subscription that is deleted takes its kept notifications along.
	hook.status.Store(http.StatusServiceUnavailable)
	mustProgram(t, vtn.BL, openadr.Program{Name: "for-nobody"})
	eventually(t, "it is kept", func() bool { return pending(t, store) == 1 })
	if _, err := vtn.BL.DeleteSubscription(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the notification of a deleted subscription was given up", func() bool { return pending(t, store) == 0 })
	hook.status.Store(0)
	time.Sleep(100 * time.Millisecond)
	if slices.Contains(hook.got(), "for-nobody") {
		t.Error("a notification was delivered for a subscription that was deleted")
	}
}

// A notification is not kept for ever.
func TestDurableNotificationsExpire(t *testing.T) {
	store := storage.NewMemory()
	vtn := start(t, options{store: store, outbox: true, redeliver: 20 * time.Millisecond, ttl: 150 * time.Millisecond,
		sender: &webhook.SenderConfig{Attempts: 1, Timeout: 500 * time.Millisecond}})
	hook := newEndpoint(t)
	if _, err := vtn.BL.CreateSubscription(context.Background(), openadr.Subscription{ClientName: "bl", ObjectOperations: []openadr.ObjectOperation{{
		Objects: []openadr.ObjectType{openadr.ObjectProgram}, Operations: []openadr.Operation{openadr.OperationCreate}, CallbackURL: hook.URL,
	}}}); err != nil {
		t.Fatal(err)
	}
	hook.status.Store(http.StatusServiceUnavailable)
	mustProgram(t, vtn.BL, openadr.Program{Name: "too-late"})
	eventually(t, "it was tried more than once", func() bool { return hook.requests.Load() >= 2 })
	eventually(t, "it was given up when it was too old", func() bool { return pending(t, store) == 0 })
	tried := hook.requests.Load()
	hook.status.Store(0)
	time.Sleep(100 * time.Millisecond)
	if hook.requests.Load() != tried || len(hook.got()) != 0 {
		t.Errorf("a notification that was given up was sent again: %v", hook.got())
	}
}

// An outbox needs a sender that says what became of a notification.
func TestOutboxNeedsAConfirmingSender(t *testing.T) {
	_, err := server.New(server.Config{Store: storage.NewMemory(), AllowAnonymous: true, Outbox: storage.NewMemory()})
	if !errors.Is(err, openadr.ErrConfiguration) {
		t.Errorf("an outbox without a sender: %v", err)
	}
}

// With NotifyReads, who subscribed to READ is told of every object that
// is read, as it may see it; without, of none.
func TestReadNotifications(t *testing.T) {
	ctx := context.Background()
	reading := func(objects ...openadr.ObjectType) []openadr.ObjectOperation {
		return []openadr.ObjectOperation{{Objects: objects, Operations: []openadr.Operation{openadr.OperationRead}, Mechanism: openadr.MechanismWebSocket}}
	}
	vtn := start(t, options{reads: true, mqtt: true, websocket: &server.WebSocketConfig{}})
	if _, err := vtn.BL.CreateVEN(ctx, openadr.VEN{Name: "site-1", ClientID: "ven-1", Targets: []string{"group-a"}}); err != nil {
		t.Fatal(err)
	}
	program := mustProgram(t, vtn.BL, openadr.Program{Name: "flex"})
	open := mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID, Name: "for-all"})
	targeted := mustEvent(t, vtn.BL, openadr.Event{ProgramID: program.ID, Name: "for-group-a", Targets: []string{"group-a"}})

	if _, err := vtn.BL.CreateSubscription(ctx, openadr.Subscription{ClientName: "bl", ObjectOperations: reading(openadr.ObjectEvent, openadr.ObjectReport)}); err != nil {
		t.Fatal(err)
	}
	if _, err := vtn.VEN2.CreateSubscription(ctx, openadr.Subscription{ClientName: "ven-2", ObjectOperations: reading(openadr.ObjectEvent)}); err != nil {
		t.Fatal(err)
	}
	bl, ven2 := listen(t, vtn.BL.NotificationStream), listen(t, vtn.VEN2.NotificationStream)
	published := len(vtn.Broker.Published())

	// A VEN reads an event by ID: Business Logic learns that it did, and
	// so does another VEN that asked, for an event it may see itself.
	if _, err := vtn.VEN1.Event(ctx, open.ID); err != nil {
		t.Fatal(err)
	}
	for who, l := range map[string]*listener{"Business Logic": bl, "ven-2": ven2} {
		n := l.next(t)
		if e, ok := n.Object.(openadr.Event); !ok || n.Operation != openadr.OperationRead || e.ID != open.ID {
			t.Errorf("%s was told %+v", who, n)
		}
	}
	// An event of a group ven-2 is not in: its reading is not for ven-2,
	// and Business Logic is shown the event with its targets, not as the
	// reader saw it.
	if _, err := vtn.VEN1.Event(ctx, targeted.ID); err != nil {
		t.Fatal(err)
	}
	n := bl.next(t)
	if e := n.Object.(openadr.Event); e.ID != targeted.ID || len(e.Targets) != 1 {
		t.Errorf("Business Logic was told %+v", n)
	}
	ven2.quiet(t)

	// A search is a read of every object it returns.
	if list, err := vtn.VEN1.Events(ctx, client.EventQuery{}); err != nil || len(list) != 1 {
		t.Fatalf("ven-1's events: %d, %v", len(list), err)
	}
	if n := bl.next(t); n.Object.(openadr.Event).ID != open.ID || n.Operation != openadr.OperationRead {
		t.Errorf("Business Logic was told %+v", n)
	}
	ven2.next(t)
	// A page of it, of what the page holds.
	if _, err := vtn.BL.Events(ctx, client.EventQuery{Page: client.Page{Skip: 1, Limit: 1}}); err != nil {
		t.Fatal(err)
	}
	if n := bl.next(t); n.Object.(openadr.Event).ID != targeted.ID {
		t.Errorf("a page of a search: Business Logic was told %+v", n)
	}
	bl.quiet(t)
	// Reports too, and what is not subscribed to is not told of.
	report, err := vtn.VEN1.CreateReport(ctx, openadr.Report{EventID: open.ID, ClientName: "c", Resources: []openadr.ReportResource{}})
	if err != nil {
		t.Fatal(err)
	}
	bl.quiet(t) // a create is not a read
	if _, err := vtn.VEN1.Report(ctx, report.ID); err != nil {
		t.Fatal(err)
	}
	if n := bl.next(t); n.ObjectType != openadr.ObjectReport || n.Operation != openadr.OperationRead {
		t.Errorf("Business Logic was told %+v", n)
	}
	if _, err := vtn.VEN1.Program(ctx, program.ID); err != nil {
		t.Fatal(err)
	}
	bl.quiet(t)
	// A read that fails is not one.
	if _, err := vtn.VEN2.Event(ctx, targeted.ID); !errors.Is(err, openadr.ErrNotFound) {
		t.Fatal(err)
	}
	bl.quiet(t)
	// MQTT has topics for changes only.
	if now := len(vtn.Broker.Published()); now != published+1 {
		t.Errorf("%d messages were published for reads; only the report's creation is one", now-published-1)
	}

	// A VTN that was not told to: the subscription is taken, and nothing
	// is said.
	quietVTN := start(t, options{websocket: &server.WebSocketConfig{}})
	p := mustProgram(t, quietVTN.BL, openadr.Program{Name: "p"})
	e := mustEvent(t, quietVTN.BL, openadr.Event{ProgramID: p.ID})
	if _, err := quietVTN.BL.CreateSubscription(ctx, openadr.Subscription{ClientName: "bl", ObjectOperations: reading(openadr.ObjectEvent)}); err != nil {
		t.Fatalf("a subscription to READ: %v", err)
	}
	l := listen(t, quietVTN.BL.NotificationStream)
	if _, err := quietVTN.VEN1.Event(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	l.quiet(t)
}

// A client that asks too much is told to slow down, and when; the others
// are not held up by it; and a client with a retry policy does as it is
// told without its caller noticing.
func TestRateLimit(t *testing.T) {
	ctx := context.Background()
	vtn := start(t, options{limiter: server.NewRateLimiter(50, 2), anonymous: true})
	mustProgram(t, vtn.BL, openadr.Program{Name: "tariff"})
	token := vtn.token(t, "ven-1")

	var limited int
	for range 6 {
		status, header, body := vtn.do(t, "GET", "/programs", token, "")
		switch status {
		case http.StatusOK:
		case http.StatusTooManyRequests:
			limited++
			if header.Get("Retry-After") != "1" || !json.Valid(body) {
				t.Errorf("a 429 with Retry-After %q and the body %s", header.Get("Retry-After"), body)
			}
		default:
			t.Fatalf("status %d", status)
		}
	}
	if limited == 0 {
		t.Fatal("six requests at once were all taken")
	}
	// The budget is the client's own.
	if _, err := vtn.VEN2.Programs(ctx, client.ProgramQuery{}); err != nil {
		t.Errorf("another client: %v", err)
	}
	// Without a policy, the caller is told, and how long to wait.
	var apiErr *openadr.Error
	var err error
	for range 6 {
		if _, err = vtn.VEN2.Programs(ctx, client.ProgramQuery{}); err != nil {
			break
		}
	}
	if !errors.Is(err, openadr.ErrTooManyRequests) || !errors.As(err, &apiErr) || apiErr.RetryAfter != time.Second {
		t.Errorf("a client that asks too much: %v", err)
	}
	// What is refused before it is a request of a client costs nothing:
	// a wrong token is still a 401, however often.
	for range 10 {
		if status, _, _ := vtn.do(t, "GET", "/programs", "not-a-token", ""); status != http.StatusUnauthorized {
			t.Fatalf("a forged token: %d", status)
		}
	}
	// Clients without a token share one budget.
	limited = 0
	for range 6 {
		if status, _, _ := vtn.do(t, "GET", "/programs", "", ""); status == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Error("requests without a token were never limited")
	}

	// With a policy: eight calls, more than the budget allows at once,
	// all succeed, because the client waits as it is told.
	patient, err := client.NewVEN(client.Config{BaseURL: vtn.URL, HTTPClient: vtn.HTTP,
		Credentials: &client.Credentials{ClientID: "ven-1", ClientSecret: "ven-1-secret"},
		RetryPolicy: client.ExponentialBackoff(time.Millisecond, time.Second, 5)})
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	for i := range 8 {
		if _, err := patient.Programs(ctx, client.ProgramQuery{}); err != nil {
			t.Fatalf("call %d of a client that waits: %v", i, err)
		}
	}
	if time.Since(began) < 900*time.Millisecond {
		t.Errorf("eight calls on a budget of two took %v: the client did not wait", time.Since(began))
	}
}

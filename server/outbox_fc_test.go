// SPDX-License-Identifier: MIT

package server_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-openadr/internal/guard/guardtest"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// holding is a ConfirmingSender that keeps every notification it is
// handed, unconfirmed, until the test confirms them.
type holding struct {
	mu     sync.Mutex
	done   []func(error)
	sent   []string
	tokens map[string]string
}

func (*holding) Verify(context.Context, string) error  { return nil }
func (h *holding) Send(url, token string, body []byte) {}

func (h *holding) SendConfirmed(url, token string, _ []byte, done func(error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.done = append(h.done, done)
	h.sent = append(h.sent, url)
	if h.tokens == nil {
		h.tokens = map[string]string{}
	}
	h.tokens[url] = token
}

// confirmAll confirms what the sender holds, and what the VTN hands it
// meanwhile, until it holds nothing.
func (h *holding) confirmAll() {
	for {
		h.mu.Lock()
		done := h.done
		h.done = nil
		h.mu.Unlock()
		if len(done) == 0 {
			return
		}
		for _, f := range done {
			f(nil)
		}
	}
}

func (h *holding) wait(t *testing.T, what string, ok func(sent []string, tokens map[string]string) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.mu.Lock()
		got := ok(h.sent, h.tokens)
		h.mu.Unlock()
		if got {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func pending(t *testing.T, box storage.Outbox) int {
	t.Helper()
	list, err := box.Deliveries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func warnings(h *guardtest.Handler, part string) int {
	n := 0
	for _, e := range h.Entries() {
		if strings.Contains(e.Message, part) {
			n++
		}
	}
	return n
}

// One write of a client whose subscriptions name 1600 callback URLs
// records no more notifications than Limits.Deliveries, with one warning;
// another client's are recorded all the same. Delivered ones make room
// again, and the bound holds for what a VTN before this one left behind.
func TestOutboxBoundsWhatOneClientRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vtn.journal")
	file, err := storage.OpenFile(path, storage.FileOptions{NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	logs := guardtest.NewHandler()
	sender := &holding{}
	const most = 500
	start := func(file *storage.File) *server.Server {
		vtn, err := server.New(server.Config{Store: file, Outbox: file, Webhooks: sender, Validator: tokens, TokenURL: "https://idp.example/token",
			Limits: server.Limits{Deliveries: most}, Logger: logs.Logger()})
		if err != nil {
			t.Fatal(err)
		}
		return vtn
	}
	vtn := start(file)
	var urls []string
	for n := range 16 {
		urls = append(urls, fmt.Sprintf(`{"objects":["VEN"],"operations":["CREATE"],"callbackUrl":"https://example.com/hook?n=%d"}`, n))
	}
	sub := `{"clientName":"c","objectOperations":[` + strings.Join(urls, ",") + `]}`
	for range 100 {
		if r := call(vtn, "POST", "/subscriptions", "ven", sub); r.status != 201 {
			t.Fatalf("subscription: %d %s", r.status, r.body)
		}
	}
	if r := call(vtn, "POST", "/subscriptions", "bl", `{"clientName":"bl","objectOperations":[{"objects":["PROGRAM"],"operations":["CREATE"],"callbackUrl":"https://example.com/bl"}]}`); r.status != 201 {
		t.Fatalf("subscription: %d %s", r.status, r.body)
	}
	const dropped = "are at the limit"
	if r := call(vtn, "POST", "/vens", "ven", `{"objectType":"VEN_VEN_REQUEST","venName":"v1"}`); r.status != 201 {
		t.Fatalf("ven: %d %s", r.status, r.body)
	}
	if n := pending(t, file); n != most {
		t.Errorf("one write recorded %d notifications, want %d", n, most)
	}
	if n := warnings(logs, dropped); n != 1 {
		t.Errorf("%d warnings of notifications past the limit, want 1", n)
	}
	if r := call(vtn, "POST", "/programs", "bl", `{"programName":"p1"}`); r.status != 201 {
		t.Fatalf("program: %d %s", r.status, r.body)
	}
	if n := pending(t, file); n != most+1 {
		t.Errorf("another client's notification was not recorded: %d pending", n)
	}

	// Delivered, they make room.
	sender.confirmAll()
	if n := pending(t, file); n != 0 {
		t.Fatalf("%d notifications are still recorded after all were confirmed", n)
	}
	if r := call(vtn, "POST", "/vens", "ven", `{"objectType":"VEN_VEN_REQUEST","venName":"v2"}`); r.status != 201 {
		t.Fatalf("ven: %d %s", r.status, r.body)
	}
	if n := pending(t, file); n != most {
		t.Errorf("after delivery a write recorded %d notifications, want %d", n, most)
	}
	if n := warnings(logs, dropped); n != 2 {
		t.Errorf("%d warnings of notifications past the limit, want 2", n)
	}

	// After a restart, what is recorded counts.
	if err := vtn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if file, err = storage.OpenFile(path, storage.FileOptions{NoSync: true}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	sender.mu.Lock()
	sender.done, sender.sent = nil, nil
	sender.mu.Unlock()
	vtn = start(file)
	defer func() { _ = vtn.Close() }()
	// The loop takes up what is recorded once it has accounted for it.
	sender.wait(t, "the recorded notifications taken up", func(sent []string, _ map[string]string) bool { return len(sent) > 0 })
	if r := call(vtn, "POST", "/vens", "ven", `{"objectType":"VEN_VEN_REQUEST","venName":"v3"}`); r.status != 201 {
		t.Fatalf("ven: %d %s", r.status, r.body)
	}
	if n := pending(t, file); n != most {
		t.Errorf("after a restart a write recorded %d notifications more", n-most)
	}
}

// Limits.DeliveryBytes bounds the bytes of what one client's
// subscriptions keep recorded.
func TestOutboxBoundsTheBytesOneClientRecords(t *testing.T) {
	mem := storage.NewMemory()
	vtn, err := server.New(server.Config{Store: mem, Outbox: mem, Webhooks: &holding{}, Validator: tokens, TokenURL: "https://idp.example/token",
		Limits: server.Limits{DeliveryBytes: 4096}, Logger: guardtest.NewHandler().Logger()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vtn.Close() }()
	if r := call(vtn, "POST", "/subscriptions", "ven", `{"clientName":"c","objectOperations":[{"objects":["VEN"],"operations":["CREATE","UPDATE"],"callbackUrl":"https://example.com/hook"}]}`); r.status != 201 {
		t.Fatalf("subscription: %d %s", r.status, r.body)
	}
	attr := strings.Repeat("x", 1000)
	for i := range 8 {
		body := fmt.Sprintf(`{"objectType":"VEN_VEN_REQUEST","venName":"v%d","attributes":[{"type":"X","values":["%s"]}]}`, i, attr)
		if r := call(vtn, "POST", "/vens", "ven", body); r.status != 201 {
			t.Fatalf("ven: %d %s", r.status, r.body)
		}
	}
	list, _ := mem.Deliveries(context.Background())
	total := 0
	for _, d := range list {
		total += len(d.Body) + len(d.CallbackURL) + len(d.BearerToken)
	}
	if len(list) == 0 || len(list) == 8 || total > 4096 {
		t.Errorf("%d notifications of %d bytes are recorded, want fewer than 8 within 4096 bytes", len(list), total)
	}
}

// A recorded notification is sent with the bearer token its subscription
// has now, and given up when the subscription no longer names its URL.
func TestOutboxFollowsTheSubscription(t *testing.T) {
	mem := storage.NewMemory()
	logs := guardtest.NewHandler()
	sender := &holding{}
	vtn, err := server.New(server.Config{Store: mem, Outbox: mem, Webhooks: sender, Validator: tokens, TokenURL: "https://idp.example/token",
		RedeliverEvery: 5 * time.Millisecond, Logger: logs.Logger()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vtn.Close() }()
	r := call(vtn, "POST", "/subscriptions", "bl", `{"clientName":"bl","objectOperations":[{"objects":["PROGRAM"],"operations":["CREATE"],"callbackUrl":"https://example.com/a","bearerToken":"new"}]}`)
	if r.status != 201 {
		t.Fatalf("subscription: %d %s", r.status, r.body)
	}
	id, _ := r.json["id"].(string)
	ctx := context.Background()
	for _, d := range []storage.Delivery{
		{ID: "d-a", SubscriptionID: id, CallbackURL: "https://example.com/a", BearerToken: "old", Body: []byte(`{}`), Created: time.Now()},
		{ID: "d-b", SubscriptionID: id, CallbackURL: "https://example.com/b", BearerToken: "old", Body: []byte(`{}`), Created: time.Now()},
	} {
		if err := mem.AddDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	sender.wait(t, "the notification to the URL the subscription names", func(_ []string, tokens map[string]string) bool {
		return tokens["https://example.com/a"] != ""
	})
	logs.Wait(t, "the notification to the other URL given up", func(entries []guardtest.Entry) bool {
		return warnings(logs, "no longer names its callback URL") > 0
	})
	sender.mu.Lock()
	token, sentB := sender.tokens["https://example.com/a"], sender.tokens["https://example.com/b"] != ""
	sender.mu.Unlock()
	if token != "new" || sentB {
		t.Errorf("sent with token %q, and to the URL that is no longer named: %v", token, sentB)
	}
	list, _ := mem.Deliveries(ctx)
	if len(list) != 1 || list[0].ID != "d-a" {
		t.Errorf("recorded: %+v", list)
	}
}

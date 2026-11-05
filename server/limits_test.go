// SPDX-License-Identifier: MIT

package server_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/server"
)

// limitedVTN serves a VTN with cfg and returns a BL and a VEN client.
func limitedVTN(t *testing.T, cfg server.Config) (*client.BL, *client.VEN) {
	t.Helper()
	ts := httptest.NewServer(newServer(t, cfg))
	t.Cleanup(ts.Close)
	bl, err := client.NewBL(client.Config{BaseURL: ts.URL, Tokens: client.StaticToken("bl")})
	if err != nil {
		t.Fatal(err)
	}
	ven, err := client.NewVEN(client.Config{BaseURL: ts.URL, Tokens: client.StaticToken("ven")})
	if err != nil {
		t.Fatal(err)
	}
	return bl, ven
}

func subscription(name string, urls ...string) openadr.Subscription {
	sub := openadr.Subscription{ClientName: name}
	for _, u := range urls {
		sub.ObjectOperations = append(sub.ObjectOperations, openadr.ObjectOperation{
			Objects: []openadr.ObjectType{openadr.ObjectEvent}, Operations: []openadr.Operation{openadr.OperationCreate}, CallbackURL: u})
	}
	return sub
}

// isLimit reports whether err is the VTN's answer to a create past a limit.
func isLimit(err error, noun string) bool {
	var apiErr *openadr.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict && errors.Is(err, openadr.ErrConflict) &&
		strings.Contains(err.Error(), noun)
}

// Each client keeps at most so many of each kind; another client, and the
// same client after a delete, may create again.
func TestLimitsPerClient(t *testing.T) {
	ctx := context.Background()
	bl, ven := limitedVTN(t, server.Config{Webhooks: fakeWebhooks{},
		Limits: server.Limits{Subscriptions: 2, VENs: 1, Resources: 2, Reports: 1}})

	var first openadr.Subscription
	for i := range 2 {
		sub, err := ven.CreateSubscription(ctx, subscription(fmt.Sprint("s", i), "https://x.example/h"))
		if err != nil {
			t.Fatal(err)
		}
		first = sub
	}
	if _, err := ven.CreateSubscription(ctx, subscription("s2", "https://x.example/h")); !isLimit(err, "2 subscriptions") {
		t.Errorf("a third subscription: %v", err)
	}
	if _, err := bl.CreateSubscription(ctx, subscription("other", "https://x.example/h")); err != nil {
		t.Errorf("another client's subscription: %v", err)
	}
	if _, err := ven.DeleteSubscription(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ven.CreateSubscription(ctx, subscription("s3", "https://x.example/h")); err != nil {
		t.Errorf("a subscription after a delete: %v", err)
	}

	v, err := ven.CreateVEN(ctx, openadr.VEN{Name: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ven.CreateVEN(ctx, openadr.VEN{Name: "v2"}); !isLimit(err, "1 VENs") {
		t.Errorf("a second VEN: %v", err)
	}
	for i := range 2 {
		if _, err := ven.CreateResource(ctx, openadr.Resource{VENID: v.ID, Name: fmt.Sprint("r", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ven.CreateResource(ctx, openadr.Resource{VENID: v.ID, Name: "r2"}); !isLimit(err, "2 resources") {
		t.Errorf("a third resource: %v", err)
	}

	program, err := bl.CreateProgram(ctx, openadr.Program{Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	event, err := bl.CreateEvent(ctx, openadr.Event{ProgramID: program.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ven.CreateReport(ctx, openadr.Report{EventID: event.ID, ClientName: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ven.CreateReport(ctx, openadr.Report{EventID: event.ID, ClientName: "c"}); !isLimit(err, "1 reports") {
		t.Errorf("a second report: %v", err)
	}
}

// The defaults: 100 subscriptions; negative is no limit; VENs, resources
// and reports have none unless asked for.
func TestLimitsDefaults(t *testing.T) {
	ctx := context.Background()
	_, ven := limitedVTN(t, server.Config{Webhooks: fakeWebhooks{}})
	for i := range 100 {
		if _, err := ven.CreateSubscription(ctx, subscription(fmt.Sprint("s", i), "https://x.example/h")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ven.CreateSubscription(ctx, subscription("s100", "https://x.example/h")); !isLimit(err, "100 subscriptions") {
		t.Errorf("subscription 101 with the defaults: %v", err)
	}
	for i := range 150 {
		if _, err := ven.CreateVEN(ctx, openadr.VEN{Name: fmt.Sprint("v", i)}); err != nil {
			t.Fatalf("VEN %d with the defaults: %v", i, err)
		}
	}

	_, unlimited := limitedVTN(t, server.Config{Webhooks: fakeWebhooks{}, Limits: server.Limits{Subscriptions: -1}})
	for i := range 101 {
		if _, err := unlimited.CreateSubscription(ctx, subscription(fmt.Sprint("s", i), "https://x.example/h")); err != nil {
			t.Fatalf("subscription %d without a limit: %v", i, err)
		}
	}
}

// Concurrent creates of one client do not both take the last place.
func TestLimitsUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	_, ven := limitedVTN(t, server.Config{Webhooks: fakeWebhooks{}, Limits: server.Limits{Subscriptions: 5}})
	var created atomic.Int64
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			if _, err := ven.CreateSubscription(ctx, subscription(fmt.Sprint("s", i), "https://x.example/h")); err == nil {
				created.Add(1)
			} else if !isLimit(err, "subscriptions") {
				t.Errorf("subscription %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	if n := created.Load(); n != 5 {
		t.Errorf("%d subscriptions created, want 5", n)
	}
}

// slowWebhooks verifies each URL in a while, and refuses those with "bad"
// in them.
type slowWebhooks struct {
	delay   time.Duration
	running atomic.Int64
	most    atomic.Int64
	calls   atomic.Int64
}

func (w *slowWebhooks) Verify(ctx context.Context, url string) error {
	w.calls.Add(1)
	n := w.running.Add(1)
	defer w.running.Add(-1)
	for {
		m := w.most.Load()
		if n <= m || w.most.CompareAndSwap(m, n) {
			break
		}
	}
	select {
	case <-time.After(w.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	if strings.Contains(url, "bad") {
		return errors.New("did not answer")
	}
	return nil
}

func (*slowWebhooks) Send(string, string, []byte) {}

// The callback URLs of a subscription: at most so many distinct ones,
// verified a few at a time, each URL once, and the first failure in the
// order of objectOperations reported.
func TestCallbackVerification(t *testing.T) {
	ctx := context.Background()
	hooks := &slowWebhooks{delay: 100 * time.Millisecond}
	_, ven := limitedVTN(t, server.Config{Webhooks: hooks, Limits: server.Limits{CallbackURLs: 8}})

	urls := make([]string, 8)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://x.example/h%d", i)
	}
	began := time.Now()
	if _, err := ven.CreateSubscription(ctx, subscription("eight", append(urls, urls[0])...)); err != nil {
		t.Fatal(err)
	}
	// Eight URLs of 100 ms each, four at a time: about 200 ms, not 800.
	if took := time.Since(began); took > 700*time.Millisecond {
		t.Errorf("verifying 8 URLs took %v", took)
	}
	if most := hooks.most.Load(); most < 2 || most > 4 {
		t.Errorf("%d verifications at once, want 2 to 4", most)
	}
	if n := hooks.calls.Load(); n != 8 {
		t.Errorf("%d verifications for 8 distinct URLs", n)
	}

	nine := append(append([]string{}, urls...), "https://x.example/h8")
	if _, err := ven.CreateSubscription(ctx, subscription("nine", nine...)); !errors.Is(err, openadr.ErrBadRequest) || !strings.Contains(err.Error(), "objectOperations[8].callbackUrl") {
		t.Errorf("nine distinct URLs: %v", err)
	}

	hooks.calls.Store(0)
	bad := []string{"https://x.example/ok", "https://x.example/bad-1", "https://x.example/bad-2"}
	if _, err := ven.CreateSubscription(ctx, subscription("bad", bad...)); !errors.Is(err, openadr.ErrBadRequest) || !strings.Contains(err.Error(), "objectOperations[1].callbackUrl") {
		t.Errorf("a refused URL: %v, want objectOperations[1] named", err)
	}
}

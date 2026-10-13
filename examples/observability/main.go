// SPDX-License-Identifier: MIT

// Observability shows what a program can know about a client and a VTN
// beside what their calls return: the hooks for metrics and for logging,
// the retry policy, and the errors to tell failures apart by.
//
// The VTN here fails on purpose: every third request is answered with 503
// before it is looked at, as a gateway under load would. The client has a
// retry policy, so its caller does not notice; the metrics and the log do.
//
// The metrics are counted in a map and printed, to keep the example free
// of dependencies. In a real program the same four methods feed a
// Prometheus counter and histogram, or OpenTelemetry instruments, with the
// operation as a label.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/auth"
	"github.com/otfabric/go-openadr/client"
	"github.com/otfabric/go-openadr/server"
	"github.com/otfabric/go-openadr/server/storage"
)

// counters is a client.Metrics, a client.AttemptMetrics and a
// server.Metrics at once: it counts what it is told, by a key.
type counters struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *counters) inc(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	c.counts[key]++
}

func (c *counters) print(title string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.counts))
	for k := range c.counts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	fmt.Println(title)
	for _, k := range keys {
		fmt.Printf("  %-44s %d\n", k, c.counts[k])
	}
}

// clientMetrics is the client's view: one outcome per call.
type clientMetrics struct{ *counters }

func (m clientMetrics) OnRequest(string)                      {}
func (m clientMetrics) OnResponse(op string, _ time.Duration) { m.inc(op + "  ok") }
func (m clientMetrics) OnTimeout(op string, _ time.Duration)  { m.inc(op + "  timeout") }
func (m clientMetrics) OnError(op string, _ time.Duration, err error) {
	// The sentinels of package openadr make a small, fixed set of reasons.
	reason := "error"
	switch {
	case errors.Is(err, openadr.ErrNotFound):
		reason = "not found"
	case errors.Is(err, openadr.ErrConflict):
		reason = "conflict"
	case errors.Is(err, openadr.ErrUnavailable):
		reason = "unavailable"
	}
	m.inc(op + "  " + reason)
}

// OnAttempt makes it an AttemptMetrics too: this is where retries show.
func (m clientMetrics) OnAttempt(_ string, attempt int, _ time.Duration, _ error) {
	if attempt > 1 {
		m.inc("(attempts beyond the first)")
	}
}

// serverMetrics is the VTN's view: one status per request.
type serverMetrics struct{ *counters }

func (m serverMetrics) OnRequest(string) {}
func (m serverMetrics) OnResponse(op string, status int, _ time.Duration) {
	m.inc(fmt.Sprintf("%s  %d", op, status))
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	issuer, err := auth.NewIssuer(auth.IssuerConfig{Clients: []auth.Client{
		{ID: "utility", Secret: "utility-secret", Scopes: auth.BusinessLogicScopes()},
	}})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	baseURL := "http://" + listener.Addr().String()
	seenByVTN := &counters{}
	vtn, err := server.New(server.Config{
		Store: storage.NewMemory(), Validator: issuer, TokenHandler: issuer, TokenURL: baseURL + "/auth/token",
		Metrics: serverMetrics{seenByVTN},
	})
	if err != nil {
		return err
	}
	// The gateway that fails: every third request, before the VTN sees it.
	var requests atomic.Int64
	gateway := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1)%3 == 0 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		vtn.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: gateway, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	seenByClient := &counters{}
	utility, err := client.NewBL(client.Config{
		BaseURL:     baseURL,
		Credentials: &client.Credentials{ClientID: "utility", ClientSecret: "utility-secret"},
		// Up to four attempts, 20 ms apart at first. What may be repeated
		// is the client's to decide, and it is careful: a create is only
		// repeated when it certainly did not happen, as with this 503.
		RetryPolicy: client.ExponentialBackoff(20*time.Millisecond, time.Second, 4),
		Metrics:     clientMetrics{seenByClient},
		// Retries are logged as warnings; with slog.LevelDebug, every call.
		Logger: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelWarn,
			// Without the time, so that the output is the same every run.
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey || a.Key == "wait" {
					return slog.Attr{}
				}
				return a
			},
		})),
	})
	if err != nil {
		return err
	}

	// A utility at work. Its caller sees no failure.
	program, err := utility.CreateProgram(ctx, openadr.Program{Name: "tariff"})
	if err != nil {
		return err
	}
	for i := range 5 {
		if _, err := utility.CreateEvent(ctx, openadr.Event{ProgramID: program.ID, Name: fmt.Sprintf("prices-%d", i)}); err != nil {
			return err
		}
	}
	if _, err := utility.Events(ctx, client.EventQuery{ProgramID: program.ID}); err != nil {
		return err
	}
	// Failures a retry cannot cure are returned at once, and can be told
	// apart without reading their text.
	_, err = utility.CreateProgram(ctx, openadr.Program{Name: "tariff"})
	fmt.Println("a second program of the same name:", errors.Is(err, openadr.ErrConflict), "-", err)
	_, err = utility.Event(ctx, "no-such-event")
	fmt.Println("an event that is not there:", errors.Is(err, openadr.ErrNotFound), "-", err)
	_, err = utility.Program(ctx, "not an id")
	fmt.Println("an ID that is none:", errors.Is(err, openadr.ErrInvalid), "-", err)

	seenByClient.print("\nwhat the client counted, per call:")
	seenByVTN.print("\nwhat the VTN counted, per request it was given:")
	return nil
}

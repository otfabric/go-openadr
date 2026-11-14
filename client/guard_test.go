// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	openadr "github.com/otfabric/go-openadr"
	"github.com/otfabric/go-openadr/internal/guard/guardtest"
)

// stopping is Metrics and AttemptMetrics whose every callback does not
// return normally.
type stopping struct{}

func (stopping) OnRequest(string)                            { guardtest.Stop() }
func (stopping) OnResponse(string, time.Duration)            { guardtest.Stop() }
func (stopping) OnError(string, time.Duration, error)        { guardtest.Stop() }
func (stopping) OnTimeout(string, time.Duration)             { guardtest.Stop() }
func (stopping) OnAttempt(string, int, time.Duration, error) { guardtest.Stop() }

// Metrics callbacks and a Logger's handler that do not return normally are
// logged, and the calls end as they would have.
func TestCallbacksThatDoNotReturnNormally(t *testing.T) {
	ctx := context.Background()
	vtn := newScript(t, 503, 200, 404, -1)
	h := guardtest.NewHandler()
	h.FailWhen(func(e guardtest.Entry) bool { return !strings.HasSuffix(e.Message, guardtest.Message) })
	bl := newBL(t, vtn, Config{RetryPolicy: fast, Metrics: stopping{}, Logger: h.Logger()})

	if _, err := bl.Programs(ctx, ProgramQuery{}); err != nil {
		t.Fatal(err)
	}
	if _, err := bl.Program(ctx, "p1"); !errors.Is(err, openadr.ErrNotFound) {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := bl.DeleteEvent(short, "e1"); !errors.Is(err, openadr.ErrTimeout) {
		t.Fatal(err)
	}
	for callback, n := range map[string]int{
		"Metrics.OnRequest": 3, "AttemptMetrics.OnAttempt": 4,
		"Metrics.OnResponse": 1, "Metrics.OnError": 1, "Metrics.OnTimeout": 1,
		// One retry, and a line for each call.
		"Logger": 4,
	} {
		if got := h.Failures(callback); got != n {
			t.Errorf("%s reported %d times, want %d", callback, got, n)
		}
	}
}

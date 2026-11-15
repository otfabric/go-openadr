// SPDX-License-Identifier: MIT

package codecs

import (
	"math"
	"runtime"
	"strings"
	"testing"

	openadr "github.com/otfabric/go-openadr"
)

// allocated returns the fewest bytes f allocates in three runs, with one
// processor, as testing.AllocsPerRun measures: what other goroutines
// allocate meanwhile is not counted.
func allocated(f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	fewest := uint64(math.MaxUint64)
	var before, after runtime.MemStats
	for range 3 {
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		fewest = min(fewest, after.TotalAlloc-before.TotalAlloc)
	}
	return fewest
}

// What decoding a notification allocates, per byte of it, for the shapes
// that cost the most for their size: many small elements. The budgets are
// about one and a half times what decoding takes with the Go releases the
// module is tested with, 1.26 and 1.27, of which 1.26 takes more for
// points: a change that costs more than that for the same
// input fails here. A transport bounds the size
// (a webhook's MaxBodyBytes, the MQTT packet, the WebSocket message), and
// so, by these factors, what one notification can cost. The race detector
// allocates more, by up to raceFactor.
func TestNotificationAllocationBudget(t *testing.T) {
	const size = 64 << 10
	head := `{"objectType":"EVENT","operation":"CREATE","object":{"id":"e1","objectType":"EVENT",` +
		`"createdDateTime":"2026-01-01T00:00:00Z","modificationDateTime":"2026-01-01T00:00:00Z","programID":"p1",`
	values := func(element string) string {
		return head + `"intervals":[{"id":0,"payloads":[{"type":"PRICE","values":[` +
			strings.Repeat(element+",", size/(len(element)+1)) + element + `]}]}]}}`
	}
	for _, tc := range []struct {
		name string
		body string
		// perByte is the budget, in bytes allocated per byte decoded.
		perByte float64
		// in301 says the body is a 3.0.1 notification too, which has no
		// event without intervals and other targets.
		in301 bool
	}{
		{"a long string", head + `"eventName":"` + strings.Repeat("a", size) + `"}}`, 4, false},
		{"unknown members", head + `"x":[` + strings.Repeat(`{},`, size/3) + `{}]}}`, 2, false},
		{"targets", head + `"targets":[` + strings.Repeat(`"a",`, size/4) + `"a"]}}`, 30, false},
		{"intervals", head + `"intervals":[` + strings.Repeat(`{"id":0,"payloads":[]},`, size/23) + `{"id":0,"payloads":[]}]}}`, 15, true},
		{"points", values(`{"x":0,"y":0}`), 45, true},
		{"numbers", values("0"), 140, true},
	} {
		for _, v := range []openadr.Version{openadr.Version310, openadr.Version301} {
			if v == openadr.Version301 && !tc.in301 {
				continue
			}
			c, err := For(v)
			if err != nil {
				t.Fatal(err)
			}
			data := []byte(strings.ReplaceAll(tc.body, `"operation":"CREATE",`, map[openadr.Version]string{
				openadr.Version310: `"operation":"CREATE",`, openadr.Version301: `"operation":"POST",`}[v]))
			var derr error
			got := allocated(func() { _, derr = c.UnmarshalNotification(data) })
			if derr != nil {
				t.Errorf("%s %s: %v", v, tc.name, derr)
				continue
			}
			ratio := float64(got) / float64(len(data))
			t.Logf("%s %s: %d bytes, %.1f per byte", v, tc.name, len(data), ratio)
			if budget := tc.perByte * raceFactor; ratio > budget {
				t.Errorf("%s %s: %.1f bytes allocated per byte of %d, budget %.0f", v, tc.name, ratio, len(data), budget)
			}
		}
	}
}

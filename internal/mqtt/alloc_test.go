// SPDX-License-Identifier: MIT

package mqtt_test

import (
	"bytes"
	"errors"
	"io"
	"math"
	"runtime"
	"testing"

	"github.com/otfabric/go-openadr/internal/mqtt"
)

// allocated returns the fewest bytes one call of the function that setup
// returns allocates, in five runs: what setup allocates is not counted. It
// measures with one processor, as testing.AllocsPerRun does: what other
// goroutines allocate meanwhile is not counted either.
func allocated(setup func() func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	fewest := uint64(math.MaxUint64)
	var before, after runtime.MemStats
	for range 5 {
		f := setup()
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		fewest = min(fewest, after.TotalAlloc-before.TotalAlloc)
	}
	return fewest
}

// remaining writes a remaining length as MQTT does, in up to four bytes,
// or in five when n needs more.
func remaining(n uint64) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		if n >>= 7; n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

// A peer that declares a length and sends nothing after costs at most that
// length, when it is one ReadPacket takes, and nothing for the body when
// it is not: the length is checked before the body is allocated.
func TestReadPacketAllocationBudget(t *testing.T) {
	// What an error and the reading take besides the body.
	const slack = 1 << 10
	for _, tc := range []struct {
		name   string
		length uint64
		budget uint64
		want   error
	}{
		{"the largest packet", mqtt.MaxPacket, mqtt.MaxPacket + slack, io.ErrUnexpectedEOF},
		{"one byte more", mqtt.MaxPacket + 1, slack, mqtt.ErrMalformed},
		{"the most four bytes say", 1<<28 - 1, slack, mqtt.ErrMalformed},
		{"five bytes", 1 << 28, slack, mqtt.ErrMalformed},
	} {
		header := append([]byte{mqtt.TypePublish << 4}, remaining(tc.length)...)
		var err error
		got := allocated(func() func() {
			r := bytes.NewReader(header)
			return func() { _, err = mqtt.ReadPacket(r) }
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if got > tc.budget {
			t.Errorf("%s: %d bytes allocated, budget %d", tc.name, got, tc.budget)
		}
	}
}

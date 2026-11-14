// SPDX-License-Identifier: MIT

package ws

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"runtime"
	"testing"
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

// header is the start of an unmasked frame, as a server sends it, with a
// 64-bit length.
func header(opcode byte, fin bool, length uint64) []byte {
	first := opcode
	if fin {
		first |= 0x80
	}
	return binary.BigEndian.AppendUint64([]byte{first, 127}, length)
}

// A peer that declares a length and sends nothing after costs at most that
// length, when it fits the message, and nothing for the payload when it
// does not: the length is checked before the payload is allocated, for a
// frame and for the frames of one message together.
func TestReadAllocationBudget(t *testing.T) {
	// What an error and the reading take besides the payload.
	const slack = 1 << 10
	const limit = DefaultMaxMessage
	var tooLarge *protocolError
	for _, tc := range []struct {
		name   string
		frames []byte
		budget uint64
		large  bool
	}{
		{"a frame of the largest message", header(opText, true, limit), limit + slack, false},
		{"one byte more", header(opText, true, limit+1), slack, true},
		{"the most 64 bits say", header(opText, true, math.MaxUint64), slack, true},
		// Half the message, and a second frame that declares one byte more
		// than the other half: the first half is all that is allocated, as
		// a frame and as the message it is added to.
		{"frames of one message, one byte more", append(append(header(opText, false, limit/2), make([]byte, limit/2)...),
			header(opContinuation, true, limit/2+1)...), 2*(limit/2) + slack, true},
	} {
		var err error
		got := allocated(func() func() {
			c := &Conn{rw: nopCloser{}, br: bufio.NewReaderSize(bytes.NewReader(tc.frames), 64<<10), client: true}
			return func() { _, err = c.ReadMessage() }
		})
		// Without the rest, the read ends with the input.
		if tc.large != errors.As(err, &tooLarge) || !tc.large && !errors.Is(err, io.EOF) {
			t.Errorf("%s: %v", tc.name, err)
		}
		if got > tc.budget {
			t.Errorf("%s: %d bytes allocated, budget %d", tc.name, got, tc.budget)
		}
	}
}

// nopCloser is a connection that takes what is written and closing.
type nopCloser struct{}

func (nopCloser) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopCloser) Write(b []byte) (int, error) { return len(b), nil }
func (nopCloser) Close() error                { return nil }

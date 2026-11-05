// SPDX-License-Identifier: MIT

package mqtt

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestCheckLength(t *testing.T) {
	if err := checkLength(MaxRemaining); err != nil {
		t.Errorf("MaxRemaining: %v", err)
	}
	if err := checkLength(MaxRemaining + 1); !errors.Is(err, ErrTooLong) {
		t.Errorf("MaxRemaining+1: %v", err)
	}
}

// A SUBSCRIBE too long for one packet is refused before it is built: the
// filters here share one string, the packet would be 268 MB.
func TestSubscribeTooLong(t *testing.T) {
	filter := strings.Repeat("a", 0xFFFF)
	filters := make([]string, MaxRemaining/(len(filter)+3)+1)
	for i := range filters {
		filters[i] = filter
	}
	if _, err := Subscribe(1, filters); !errors.Is(err, ErrTooLong) {
		t.Errorf("%d filters of 64 KiB: %v", len(filters), err)
	}
	if _, err := Subscribe(1, filters[:1]); err != nil {
		t.Errorf("one filter of 64 KiB: %v", err)
	}
}

// A connection that fails when CONNECT is written is reported, and closed.
func TestDialWriteFails(t *testing.T) {
	saved := dial
	t.Cleanup(func() { dial = saved })
	dial = func(context.Context, string) (net.Conn, error) {
		c, p := net.Pipe()
		_ = p.Close()
		return c, nil
	}
	_, err := Dial(context.Background(), "mqtt://broker.invalid", Options{})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Dial: %v, want io.ErrClosedPipe", err)
	}
}

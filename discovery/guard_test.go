// SPDX-License-Identifier: MIT

package discovery

import (
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/otfabric/go-openadr/internal/guard/guardtest"
)

// unsendable is a socket on which nothing can be sent.
type unsendable struct{ net.PacketConn }

func (unsendable) WriteTo([]byte, net.Addr) (int, error) { return 0, errors.New("the link is down") }

// downHost is a host whose link takes nothing.
type downHost struct{ fakeHost }

func (h downHost) member(l link) (net.PacketConn, error) {
	c, err := h.fakeHost.member(l)
	return unsendable{c}, err
}

// A handler of the Logger that does not return normally loses its entry,
// which is reported, and the Advertiser goes on.
func TestLoggerThatDoesNotReturnNormally(t *testing.T) {
	n := newFakeNet("10.0.0.1")
	h := guardtest.NewHandler()
	h.FailWhen(func(e guardtest.Entry) bool { return !strings.HasSuffix(e.Message, guardtest.Message) })
	ad := advertise(t, downHost{n.host(0).(fakeHost)}, Advertisement{Instance: "Gate", Host: "gate", Port: 8443, Logger: h.Logger()})
	h.WaitFailures(t, "Logger", 1)
	if entries := h.Entries(); !strings.Contains(entries[0].Message, "cannot send") {
		t.Errorf("the lost entry: %+v", entries[0])
	}
	if err := ad.Close(); err != nil {
		t.Error(err)
	}
}

// L1, L2: Close from several goroutines at once, and again after.
func TestAdvertiserCloseFromSeveralGoroutines(t *testing.T) {
	n := newFakeNet("10.0.0.1")
	for range 50 {
		ad := advertise(t, n.host(0), Advertisement{Instance: "Gate", Host: "gate", Port: 8443})
		var start, all sync.WaitGroup
		start.Add(1)
		for range 8 {
			all.Go(func() {
				start.Wait()
				_ = ad.Close()
			})
		}
		start.Done()
		all.Wait()
		if err := ad.Close(); err != nil {
			t.Errorf("Close again: %v", err)
		}
	}
}

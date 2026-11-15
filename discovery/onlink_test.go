package discovery

import (
	"net"
	"net/netip"
	"testing"
)

func TestOnLink(t *testing.T) {
	l := link{nets: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}}
	for _, c := range []struct {
		addr net.Addr
		want bool
	}{
		{&net.UDPAddr{IP: net.ParseIP("192.168.1.7"), Port: 5353}, true},
		{&net.UDPAddr{IP: net.ParseIP("169.254.10.20"), Port: 5353}, true},
		{&net.UDPAddr{IP: net.ParseIP("::ffff:169.254.10.20"), Port: 5353}, true},
		{&net.UDPAddr{IP: net.ParseIP("192.168.2.7"), Port: 5353}, false},
		{&net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5353}, false},
		{&net.UDPAddr{IP: []byte{1, 2, 3}, Port: 5353}, false},
		{&net.TCPAddr{IP: net.ParseIP("192.168.1.7"), Port: 5353}, false},
	} {
		if got := l.onLink(c.addr); got != c.want {
			t.Errorf("onLink(%v) = %v, want %v", c.addr, got, c.want)
		}
	}
}

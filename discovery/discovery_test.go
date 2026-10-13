// SPDX-License-Identifier: MIT

package discovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- a network in memory

type packet struct {
	data []byte
	from net.Addr
}

// fakeNet is a network of one link per address given, on which what is
// sent to the group reaches every member and the rest reaches its address.
type fakeNet struct {
	mu    sync.Mutex
	hosts []link
	socks []*fakeSock
	next  int
	// lose is called for every packet; one it returns true for is lost.
	lose func(data []byte, to net.Addr) bool
	// sent records every packet, for tests that look at the traffic.
	sent []packet
}

func newFakeNet(addrs ...string) *fakeNet {
	n := &fakeNet{next: 40000}
	for i, a := range addrs {
		ip := netip.MustParseAddr(a)
		n.hosts = append(n.hosts, link{
			name: "fake" + string(rune('0'+i)), addrs: []netip.Addr{ip},
			nets: []netip.Prefix{netip.PrefixFrom(ip, 24).Masked()}, ip4: ip.AsSlice(),
		})
	}
	return n
}

// host returns the network as one of its hosts sees it.
func (n *fakeNet) host(i int) network { return fakeHost{n, n.hosts[i]} }

type fakeHost struct {
	net  *fakeNet
	link link
}

func (h fakeHost) links() ([]link, error) { return []link{h.link}, nil }

func (h fakeHost) member(l link) (net.PacketConn, error) { return h.net.open(l, 5353, true), nil }

func (h fakeHost) asker(l link) (net.PacketConn, error) {
	h.net.mu.Lock()
	h.net.next++
	port := h.net.next
	h.net.mu.Unlock()
	return h.net.open(l, port, false), nil
}

func (n *fakeNet) open(l link, port int, member bool) *fakeSock {
	s := &fakeSock{net: n, addr: &net.UDPAddr{IP: l.ip4, Port: port}, member: member, in: make(chan packet, 256), closed: make(chan struct{})}
	n.mu.Lock()
	n.socks = append(n.socks, s)
	n.mu.Unlock()
	return s
}

type fakeSock struct {
	net    *fakeNet
	addr   *net.UDPAddr
	member bool
	in     chan packet
	closed chan struct{}
	once   sync.Once
}

func (s *fakeSock) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case p := <-s.in:
		return copy(b, p.data), p.from, nil
	case <-s.closed:
		return 0, nil, net.ErrClosed
	}
}

func (s *fakeSock) WriteTo(b []byte, to net.Addr) (int, error) {
	select {
	case <-s.closed:
		return 0, net.ErrClosed
	default:
	}
	dst := to.(*net.UDPAddr)
	p := packet{data: append([]byte(nil), b...), from: s.addr}
	s.net.mu.Lock()
	defer s.net.mu.Unlock()
	s.net.sent = append(s.net.sent, packet{data: p.data, from: to})
	if s.net.lose != nil && s.net.lose(b, to) {
		return len(b), nil
	}
	for _, o := range s.net.socks {
		hit := o.addr.IP.Equal(dst.IP) && o.addr.Port == dst.Port
		if dst.IP.Equal(group.IP) {
			hit = o.member && dst.Port == group.Port
		}
		if hit {
			select {
			case o.in <- p:
			default:
			}
		}
	}
	return len(b), nil
}

func (s *fakeSock) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *fakeSock) LocalAddr() net.Addr              { return s.addr }
func (s *fakeSock) SetDeadline(time.Time) error      { return nil }
func (s *fakeSock) SetReadDeadline(time.Time) error  { return nil }
func (s *fakeSock) SetWriteDeadline(time.Time) error { return nil }

func advertise(t *testing.T, n network, a Advertisement) *Advertiser {
	t.Helper()
	a.net = n
	ad, err := Advertise(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ad.Close() })
	return ad
}

func browse(t *testing.T, n network, o Options) []VTN {
	t.Helper()
	o.net = n
	if o.Timeout == 0 {
		o.Timeout = 300 * time.Millisecond
	}
	vtns, err := Browse(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return vtns
}

// --- tests

func TestAdvertiseAndBrowse(t *testing.T) {
	n := newFakeNet("192.168.1.42", "192.168.1.50")
	advertise(t, n.host(0), Advertisement{
		Instance: "My VTN", Host: "myvtn.local", Port: 883, BasePath: "/openadr3/3.1.0/",
		ProgramNames: []string{"local", "tariff"}, OpenAPIURL: "https://myvtn.local:883/openapi.yaml",
	})
	vtns := browse(t, n.host(1), Options{})
	if len(vtns) != 1 {
		t.Fatalf("found %d VTNs: %+v", len(vtns), vtns)
	}
	got := vtns[0]
	want := VTN{
		Instance: "My VTN", Host: "myvtn.local", Port: 883, Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.42")},
		Version: "3.1.0", BasePath: "openadr3/3.1.0", LocalURL: "https://myvtn.local:883/openadr3/3.1.0",
		ProgramNames: []string{"local", "tariff"}, RequiresAuth: true, OpenAPIURL: "https://myvtn.local:883/openapi.yaml",
		Text: map[string]string{
			"version": "3.1.0", "base_path": "openadr3/3.1.0", "local_url": "https://myvtn.local:883/openadr3/3.1.0",
			"program_names": "local,tariff", "requires_auth": "True", "openapi_url": "https://myvtn.local:883/openapi.yaml",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if got.URL() != "https://myvtn.local:883/openadr3/3.1.0" {
		t.Errorf("URL %q", got.URL())
	}
}

func TestBrowseSeveralAndFirst(t *testing.T) {
	n := newFakeNet("10.0.0.1", "10.0.0.2", "10.0.0.3")
	advertise(t, n.host(0), Advertisement{Instance: "B gateway", Host: "b", Port: 8443, NoAuth: true})
	advertise(t, n.host(1), Advertisement{Instance: "A gateway", Host: "a", Port: 443})

	vtns := browse(t, n.host(2), Options{})
	if len(vtns) != 2 || vtns[0].Instance != "A gateway" || vtns[1].Instance != "B gateway" {
		t.Fatalf("found %+v", vtns)
	}
	if vtns[0].RequiresAuth != true || vtns[1].RequiresAuth != false {
		t.Errorf("requires_auth: %v, %v", vtns[0].RequiresAuth, vtns[1].RequiresAuth)
	}
	if got := vtns[1].URL(); got != "https://b.local:8443" {
		t.Errorf("URL %q", got)
	}

	start := time.Now()
	first := browse(t, n.host(2), Options{First: true, Timeout: 5 * time.Second})
	if len(first) == 0 || time.Since(start) > 2*time.Second {
		t.Errorf("First: %d VTNs after %v", len(first), time.Since(start))
	}
}

func TestBrowseFindsNothing(t *testing.T) {
	n := newFakeNet("10.0.0.1")
	if vtns := browse(t, n.host(0), Options{Timeout: 50 * time.Millisecond}); len(vtns) != 0 {
		t.Errorf("found %+v", vtns)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Browse(ctx, Options{net: n.host(0)}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled browse: %v", err)
	}
}

func TestGoodbye(t *testing.T) {
	n := newFakeNet("10.0.0.1", "10.0.0.2")
	ad := advertise(t, n.host(0), Advertisement{Port: 443})
	if len(browse(t, n.host(1), Options{})) != 1 {
		t.Fatal("not found while advertised")
	}
	// A listener hears the goodbye.
	listener := n.open(n.hosts[1], 5353, true)
	if err := ad.Close(); err != nil {
		t.Fatal(err)
	}
	_ = ad.Close()
	deadline := time.After(time.Second)
	for {
		select {
		case p := <-listener.in:
			m, err := parseMessage(p.data)
			if err != nil || len(m.answers) == 0 || m.answers[0].ttl != 0 {
				continue
			}
			for _, r := range m.answers {
				if r.ttl != 0 {
					t.Errorf("a goodbye with a record that lives %d s", r.ttl)
				}
			}
		case <-deadline:
			t.Fatal("no goodbye was sent")
		}
		break
	}
	if vtns := browse(t, n.host(1), Options{Timeout: 100 * time.Millisecond}); len(vtns) != 0 {
		t.Errorf("still found after Close: %+v", vtns)
	}
}

func TestBrowseSurvivesLoss(t *testing.T) {
	n := newFakeNet("10.0.0.1", "10.0.0.2")
	advertise(t, n.host(0), Advertisement{Port: 443})
	// The first two queries are lost on the way.
	var queries int
	n.lose = func(data []byte, _ net.Addr) bool {
		if m, err := parseMessage(data); err == nil && !m.response {
			queries++
			return queries <= 2
		}
		return false
	}
	if vtns := browse(t, n.host(1), Options{Timeout: 3 * time.Second, First: true}); len(vtns) != 1 {
		t.Errorf("found %+v", vtns)
	}
}

// A responder that tells no more than it is asked: the browser has to ask
// for the rest.
func TestBrowseAsksForWhatIsMissing(t *testing.T) {
	n := newFakeNet("10.0.0.1", "10.0.0.2")
	sock := n.open(n.hosts[0], 5353, true)
	instance := append(name{"Terse"}, serviceName...)
	host := name{"terse", "local"}
	go func() {
		buf := make([]byte, maxMessage)
		for {
			k, from, err := sock.ReadFrom(buf)
			if err != nil {
				return
			}
			q, err := parseMessage(buf[:k])
			if err != nil || q.response {
				continue
			}
			reply := message{response: true, id: q.id, questions: q.questions}
			for _, question := range q.questions {
				switch {
				case question.name.equal(serviceName):
					reply.answers = append(reply.answers, record{name: serviceName, rtype: typePTR, ttl: 10, target: instance})
				case question.name.equal(instance) && question.qtype == typeSRV:
					reply.answers = append(reply.answers, record{name: instance, rtype: typeSRV, ttl: 10, target: host, port: 8443})
				case question.name.equal(instance) && question.qtype == typeTXT:
					reply.answers = append(reply.answers, record{name: instance, rtype: typeTXT, ttl: 10, text: []string{"VERSION=3.1.0", "version=9", "requires_auth=false", "novalue", "=nokey"}})
				case question.name.equal(host) && question.qtype == typeA:
					reply.answers = append(reply.answers, record{name: host, rtype: typeA, ttl: 10, addr: netip.MustParseAddr("10.0.0.1")})
				}
			}
			b, _ := reply.marshal()
			_, _ = sock.WriteTo(b, from)
		}
	}()
	defer func() { _ = sock.Close() }()

	vtns := browse(t, n.host(1), Options{Timeout: 5 * time.Second, First: true})
	if len(vtns) != 1 {
		t.Fatalf("found %+v", vtns)
	}
	v := vtns[0]
	if v.Instance != "Terse" || v.Host != "terse.local" || v.Port != 8443 || len(v.Addrs) != 1 {
		t.Errorf("got %+v", v)
	}
	// Keys are case-insensitive and the first wins; a key without a value
	// is kept, an entry without a key is not.
	if v.Version != "3.1.0" || v.RequiresAuth || v.URL() != "https://terse.local:8443" {
		t.Errorf("got %+v", v)
	}
	if _, ok := v.Text["novalue"]; !ok || len(v.Text) != 3 {
		t.Errorf("text %v", v.Text)
	}
}

// query sends a query from a socket and returns the first answer to it.
func query(t *testing.T, sock *fakeSock, m message, wait time.Duration) (message, bool) {
	t.Helper()
	b, err := m.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sock.WriteTo(b, group); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(wait)
	for {
		select {
		case p := <-sock.in:
			if reply, err := parseMessage(p.data); err == nil && reply.response {
				return reply, true
			}
		case <-deadline:
			return message{}, false
		}
	}
}

func TestResponder(t *testing.T) {
	n := newFakeNet("10.0.0.1", "10.0.0.2", "172.16.0.9")
	ad := advertise(t, n.host(0), Advertisement{Instance: "Gate", Host: "gate", Port: 8443})
	// Let the announcements pass, so that what is read below is an answer.
	time.Sleep(1200 * time.Millisecond)
	browseQ := question{name: serviceName, qtype: typePTR}

	t.Run("a one-shot resolver is answered directly, as a DNS server would", func(t *testing.T) {
		sock := n.open(n.hosts[1], 41000, false)
		reply, ok := query(t, sock, message{id: 0xBEEF, questions: []question{browseQ}}, time.Second)
		if !ok {
			t.Fatal("no answer")
		}
		if reply.id != 0xBEEF || len(reply.questions) != 1 || !reply.questions[0].name.equal(serviceName) {
			t.Errorf("id %#x, questions %+v", reply.id, reply.questions)
		}
		if len(reply.answers) != 1 || len(reply.additional) != 3 {
			t.Fatalf("%d answers, %d additional", len(reply.answers), len(reply.additional))
		}
		for _, r := range append(reply.answers, reply.additional...) {
			if r.ttl > 10 || r.unique {
				t.Errorf("record %v: ttl %d, cache-flush %v", r.name, r.ttl, r.unique)
			}
		}
	})

	t.Run("a Multicast DNS querier is answered on the group", func(t *testing.T) {
		sock := n.open(n.hosts[1], 5353, true)
		defer func() { _ = sock.Close() }()
		reply, ok := query(t, sock, message{questions: []question{browseQ}}, time.Second)
		if !ok {
			t.Fatal("no answer")
		}
		if reply.id != 0 || len(reply.questions) != 0 {
			t.Errorf("id %#x, %d questions", reply.id, len(reply.questions))
		}
		var srv record
		for _, r := range reply.additional {
			if r.rtype == typeSRV {
				srv = r
			}
		}
		if reply.answers[0].ttl != ttlOther || reply.answers[0].unique || !srv.unique || srv.ttl != ttlHost || srv.port != 8443 {
			t.Errorf("ptr %+v, srv %+v", reply.answers[0], srv)
		}
	})

	t.Run("who knows the answer already is not told again", func(t *testing.T) {
		sock := n.open(n.hosts[1], 5353, true)
		defer func() { _ = sock.Close() }()
		known := message{questions: []question{browseQ}, answers: []record{ad.ptr()}}
		if reply, ok := query(t, sock, known, 200*time.Millisecond); ok {
			t.Errorf("answered: %+v", reply)
		}
	})

	t.Run("questions about the instance, the host and the service types", func(t *testing.T) {
		sock := n.open(n.hosts[1], 41001, false)
		tests := []struct {
			q    question
			want uint16
		}{
			{question{name: ad.instance, qtype: typeSRV}, typeSRV},
			{question{name: ad.instance, qtype: typeTXT}, typeTXT},
			{question{name: ad.host, qtype: typeA}, typeA},
			{question{name: ad.host, qtype: typeANY}, typeA},
			{question{name: name{"GATE", "LOCAL"}, qtype: typeA}, typeA},
			{question{name: metaName, qtype: typePTR}, typePTR},
			{question{name: ad.instance, qtype: typeANY}, typeSRV},
		}
		for _, tt := range tests {
			reply, ok := query(t, sock, message{questions: []question{tt.q}}, time.Second)
			if !ok || len(reply.answers) == 0 || reply.answers[0].rtype != tt.want {
				t.Errorf("%v type %d: %+v", tt.q.name, tt.q.qtype, reply.answers)
			}
		}
	})

	t.Run("what is not about this VTN is not answered", func(t *testing.T) {
		sock := n.open(n.hosts[1], 41002, false)
		for _, q := range []question{
			{name: name{"_http", "_tcp", "local"}, qtype: typePTR},
			{name: name{"other", "local"}, qtype: typeA},
			{name: ad.host, qtype: typeAAAA}, // the host has no IPv6 address
			{name: serviceName, qtype: typeSRV},
		} {
			if reply, ok := query(t, sock, message{questions: []question{q}}, 100*time.Millisecond); ok {
				t.Errorf("%v type %d was answered: %+v", q.name, q.qtype, reply.answers)
			}
		}
	})

	t.Run("a query from another network is not answered", func(t *testing.T) {
		sock := n.open(n.hosts[2], 41003, false)
		if reply, ok := query(t, sock, message{questions: []question{browseQ}}, 200*time.Millisecond); ok {
			t.Errorf("answered: %+v", reply)
		}
	})

	t.Run("noise is ignored", func(t *testing.T) {
		sock := n.open(n.hosts[1], 41004, false)
		for _, junk := range [][]byte{nil, {1, 2, 3}, make([]byte, 12), []byte(strings.Repeat("\xff", 64))} {
			_, _ = sock.WriteTo(junk, group)
		}
		// A response is not a query.
		b, _ := message{response: true, answers: []record{ad.ptr()}}.marshal()
		_, _ = sock.WriteTo(b, group)
		// And it still answers.
		if _, ok := query(t, sock, message{questions: []question{browseQ}}, time.Second); !ok {
			t.Error("no answer after noise")
		}
	})
}

func TestAdvertiseValidates(t *testing.T) {
	n := newFakeNet("10.0.0.1").host(0)
	tests := map[string]Advertisement{
		"Port is required":       {},
		"Advertisement.Instance": {Port: 1, Instance: strings.Repeat("x", 64)},
		"Advertisement.Host":     {Port: 1, Host: "..."},
		"holds a comma":          {Port: 1, ProgramNames: []string{"a,b"}},
		"program_names does not": {Port: 1, ProgramNames: strings.Split(strings.Repeat("program-name ", 30), " ")[:30]},
		"local_url does not fit": {Port: 1, LocalURL: "https://" + strings.Repeat("x", 300)},
	}
	for want, a := range tests {
		a.net = n
		if ad, err := Advertise(a); err == nil || !strings.Contains(err.Error(), want) {
			if ad != nil {
				_ = ad.Close()
			}
			t.Errorf("got %v, want an error that says %q", err, want)
		}
	}
	// The name of the machine, when no host is given.
	ad := advertise(t, n, Advertisement{Port: 1})
	hostname, _ := os.Hostname()
	if want := hostLabel(hostname); want != "" && ad.host[0] != want {
		t.Errorf("host %q, want %q", ad.host[0], want)
	}
}

func TestHostLabel(t *testing.T) {
	for in, want := range map[string]string{
		"MyVTN.example.com": "myvtn", "bart's mac": "bart-s-mac", "_x_": "x", "": "", "a.local": "a",
		strings.Repeat("a", 70): strings.Repeat("a", 63),
	} {
		if got := hostLabel(in); got != want {
			t.Errorf("hostLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

type noNet struct{ err error }

func (n noNet) links() ([]link, error) {
	if n.err != nil {
		return nil, n.err
	}
	return []link{{name: "dead"}}, nil
}
func (noNet) member(link) (net.PacketConn, error) { return nil, errors.New("no socket") }
func (noNet) asker(link) (net.PacketConn, error)  { return nil, errors.New("no socket") }

func TestNoNetwork(t *testing.T) {
	gone := errors.New("no interfaces")
	if _, err := Advertise(Advertisement{Port: 1, net: noNet{err: gone}}); !errors.Is(err, gone) {
		t.Errorf("Advertise: %v", err)
	}
	if _, err := Browse(context.Background(), Options{net: noNet{err: gone}}); !errors.Is(err, gone) {
		t.Errorf("Browse: %v", err)
	}
	if _, err := Advertise(Advertisement{Port: 1, net: noNet{}}); err == nil || !strings.Contains(err.Error(), "no socket") {
		t.Errorf("Advertise: %v", err)
	}
	if _, err := Browse(context.Background(), Options{net: noNet{}}); err == nil || !strings.Contains(err.Error(), "no socket") {
		t.Errorf("Browse: %v", err)
	}
}

// Over the sockets of this machine. Multicast is not to be had everywhere
// (containers, some CI runners), so finding nothing skips the test unless
// OPENADR_TEST_MDNS is set.
func TestRealNetwork(t *testing.T) {
	must := os.Getenv("OPENADR_TEST_MDNS") != ""
	ad, err := Advertise(Advertisement{Instance: "go-openadr test " + time.Now().Format("150405.000"), Port: 8443, BasePath: "openadr3", NoAuth: true})
	if err != nil {
		if must {
			t.Fatal(err)
		}
		t.Skip("cannot advertise here: ", err)
	}
	defer func() { _ = ad.Close() }()
	vtns, err := Browse(context.Background(), Options{Timeout: 4 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vtns {
		if v.Instance == ad.instance[0] {
			if v.Port != 8443 || v.BasePath != "openadr3" || v.RequiresAuth || !strings.HasSuffix(v.Host, ".local") {
				t.Errorf("found %+v", v)
			}
			t.Logf("found %s at %s %v", v.Instance, v.URL(), v.Addrs)
			return
		}
	}
	if must {
		t.Fatalf("the VTN was not found; found %+v", vtns)
	}
	t.Skip("multicast does not reach this machine's own sockets")
}

// A responder that answers every query to the whole network and never
// directly, whatever port the query came from, as that of OpenLEADR does:
// a browser that only waited for a direct answer would not hear it.
func TestBrowseHearsMulticastAnswers(t *testing.T) {
	n := newFakeNet("10.0.0.1", "10.0.0.2")
	sock := n.open(n.hosts[0], 5353, true)
	defer func() { _ = sock.Close() }()
	instance := append(name{"Loud"}, serviceName...)
	host := name{"loud", "local"}
	go func() {
		buf := make([]byte, maxMessage)
		for {
			k, _, err := sock.ReadFrom(buf)
			if err != nil {
				return
			}
			q, err := parseMessage(buf[:k])
			if err != nil || q.response || len(q.questions) == 0 || !q.questions[0].name.equal(serviceName) {
				continue
			}
			reply := message{response: true, answers: []record{
				{name: serviceName, rtype: typePTR, ttl: 4500, target: instance},
				{name: instance, rtype: typeSRV, unique: true, ttl: 120, target: host, port: 3000},
				{name: instance, rtype: typeTXT, unique: true, ttl: 4500, text: []string{"version=3.1", "local_url=http://loud.local.:3000/"}},
				{name: host, rtype: typeA, unique: true, ttl: 120, addr: netip.MustParseAddr("10.0.0.1")},
			}}
			b, _ := reply.marshal()
			_, _ = sock.WriteTo(b, group)
		}
	}()
	vtns := browse(t, n.host(1), Options{Timeout: 5 * time.Second, First: true})
	if len(vtns) != 1 || vtns[0].Instance != "Loud" || vtns[0].Port != 3000 || vtns[0].URL() != "http://loud.local.:3000/" || !vtns[0].RequiresAuth {
		t.Errorf("found %+v", vtns)
	}
}

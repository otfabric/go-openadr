// SPDX-License-Identifier: MIT

package discovery

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	instance := append(name{"My VTN. With dots"}, serviceName...)
	host := name{"myvtn", "local"}
	in := message{
		id: 7, response: true,
		questions: []question{{name: serviceName, qtype: typePTR, unicast: true}},
		answers: []record{
			{name: serviceName, rtype: typePTR, ttl: 4500, target: instance},
			{name: instance, rtype: typeSRV, unique: true, ttl: 120, target: host, port: 883},
		},
		additional: []record{
			{name: instance, rtype: typeTXT, unique: true, ttl: 4500, text: []string{"version=3.1.0", "", "requires_auth=False"}},
			{name: host, rtype: typeA, unique: true, ttl: 120, addr: netip.MustParseAddr("192.168.1.42")},
			{name: host, rtype: typeAAAA, unique: true, ttl: 120, addr: netip.MustParseAddr("fd00::1")},
		},
	}
	b, err := in.marshal()
	if err != nil {
		t.Fatal(err)
	}
	out, err := parseMessage(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("got  %+v\nwant %+v", out, in)
	}
	if instance.String() != "My VTN. With dots._openadr3._tcp.local." {
		t.Errorf("name %q", instance.String())
	}
}

func TestMarshalRefuses(t *testing.T) {
	long := strings.Repeat("x", 64)
	bad := []message{
		{questions: []question{{name: name{long}, qtype: typeA}}},
		{questions: []question{{name: name{""}, qtype: typeA}}},
		{questions: []question{{name: strings.Split(strings.Repeat("abcdefgh.", 40), "."), qtype: typeA}}},
		{answers: []record{{name: name{"a"}, rtype: typeTXT, text: []string{strings.Repeat("x", 256)}}}},
		{answers: []record{{name: name{"a"}, rtype: typeA, addr: netip.MustParseAddr("::1")}}},
		{answers: []record{{name: name{"a"}, rtype: typeAAAA, addr: netip.MustParseAddr("1.2.3.4")}}},
		{answers: []record{{name: name{"a"}, rtype: 99}}},
		{answers: []record{{name: name{"a"}, rtype: typePTR, target: name{long}}}},
	}
	for i, m := range bad {
		if _, err := m.marshal(); err == nil {
			t.Errorf("message %d was written", i)
		}
	}
	// An empty TXT record is one empty string.
	b, err := message{answers: []record{{name: name{"a"}, rtype: typeTXT}}}.marshal()
	if err != nil || b[len(b)-1] != 0 || b[len(b)-2] != 1 {
		t.Errorf("empty TXT: % x, %v", b, err)
	}
}

// A response as other implementations write it: with compression, with
// records this package does not know, and with an authority section.
func TestParseCompressed(t *testing.T) {
	msg := []byte{
		0, 0, 0x84, 0, 0, 0, 0, 2, 0, 1, 0, 2, // header: 2 answers, 1 authority, 2 additional
		// 12: _openadr3._tcp.local PTR -> X._openadr3._tcp.local (pointer to 12)
		9, '_', 'o', 'p', 'e', 'n', 'a', 'd', 'r', '3', 4, '_', 't', 'c', 'p', 5, 'l', 'o', 'c', 'a', 'l', 0,
		0, 12, 0, 1, 0, 0, 0x11, 0x94, 0, 4, 1, 'X', 0xC0, 12,
		// 46: X... (pointer to the PTR data at 44) NSEC, unknown here
		0xC0, 44, 0, 47, 0x80, 1, 0, 0, 0, 120, 0, 2, 0xC0, 44,
		// authority: skipped whatever it is
		0xC0, 44, 0, 33, 0, 1, 0, 0, 0, 120, 0, 10, 0, 0, 0, 0, 0, 80, 1, 'h', 0xC0, 27,
		// additional: X SRV -> h.local (pointer to "local" at 27), port 883
		0xC0, 44, 0, 33, 0x80, 1, 0, 0, 0, 120, 0, 10, 0, 0, 0, 0, 3, 0x73, 1, 'h', 0xC0, 27,
		// additional: X TXT "a=b"
		0xC0, 44, 0, 16, 0x80, 1, 0, 0, 0x11, 0x94, 0, 4, 3, 'a', '=', 'b',
	}
	m, err := parseMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.answers) != 1 || len(m.additional) != 2 {
		t.Fatalf("%d answers, %d additional", len(m.answers), len(m.additional))
	}
	instance := append(name{"X"}, serviceName...)
	if !m.answers[0].target.equal(instance) || !m.additional[0].name.equal(instance) {
		t.Errorf("names %v, %v", m.answers[0].target, m.additional[0].name)
	}
	if srv := m.additional[0]; srv.port != 883 || srv.target.String() != "h.local." || !srv.unique {
		t.Errorf("srv %+v", srv)
	}
	if txt := m.additional[1]; len(txt.text) != 1 || txt.text[0] != "a=b" {
		t.Errorf("txt %+v", txt)
	}
}

func TestParseMalformed(t *testing.T) {
	header := func(counts ...byte) []byte {
		h := make([]byte, 12)
		for i, c := range counts {
			h[5+2*i] = c
		}
		return h
	}
	loop := append(header(1), 0xC0, 12, 0, 1, 0, 1)
	bad := map[string][]byte{
		"short":                           {1, 2, 3},
		"a question that is missing":      header(1),
		"a name that points at itself":    loop,
		"a name that points past the end": append(header(1), 0xC0, 99, 0, 1, 0, 1),
		"a pointer cut in half":           append(header(1), 0xC0),
		"a label past the end":            append(header(1), 5, 'a'),
		"a reserved label type":           append(header(1), 0x80, 0, 0, 1, 0, 1),
		"a question without type":         append(header(1), 1, 'a', 0, 0),
		"a record without header":         append(header(0, 1), 1, 'a', 0, 0, 1),
		"data past the end":               append(header(0, 1), 1, 'a', 0, 0, 1, 0, 1, 0, 0, 0, 1, 0, 9, 1),
		"an A record of 3 bytes":          append(header(0, 1), 1, 'a', 0, 0, 1, 0, 1, 0, 0, 0, 1, 0, 3, 1, 2, 3),
		"an AAAA record of 4 bytes":       append(header(0, 1), 1, 'a', 0, 0, 28, 0, 1, 0, 0, 0, 1, 0, 4, 1, 2, 3, 4),
		"a SRV record without target":     append(header(0, 1), 1, 'a', 0, 0, 33, 0, 1, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 80),
		"a TXT entry past its record":     append(header(0, 1), 1, 'a', 0, 0, 16, 0, 1, 0, 0, 0, 1, 0, 2, 5, 'a'),
		"a PTR target past its record":    append(header(0, 1), 1, 'a', 0, 0, 12, 0, 1, 0, 0, 0, 1, 0, 2, 5, 'a', 0, 0, 0, 0),
		"another opcode":                  {0, 0, 0x28, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		"a name longer than 255 bytes":    append(append(header(1), []byte(strings.Repeat("\x3f"+strings.Repeat("a", 63), 5))...), 0, 0, 1, 0, 1),
	}
	for what, msg := range bad {
		if _, err := parseMessage(msg); !errors.Is(err, errMalformed) {
			t.Errorf("%s: got %v", what, err)
		}
	}
	if _, err := parseMessage(make([]byte, maxMessage+1)); !errors.Is(err, errMalformed) {
		t.Errorf("an oversized message: %v", err)
	}
	// A question of another class is passed over, not refused.
	m, err := parseMessage(append(header(1), 1, 'a', 0, 0, 1, 0, 3))
	if err != nil || len(m.questions) != 0 {
		t.Errorf("class CH: %+v, %v", m, err)
	}
}

func FuzzParseMessage(f *testing.F) {
	seed, _ := message{
		response: true,
		answers:  []record{{name: serviceName, rtype: typePTR, ttl: 10, target: name{"x", "local"}}, {name: name{"x"}, rtype: typeTXT, text: []string{"a=b"}}},
	}.marshal()
	f.Add(seed)
	f.Add([]byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xC0, 12, 0, 1, 0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := parseMessage(data)
		if err != nil {
			return
		}
		// What was understood can be written again, unless a name in it
		// is one this package would not write (an empty label cannot be
		// read, an over-long name is refused on reading).
		if _, err := m.marshal(); err != nil && !strings.Contains(err.Error(), "too large") {
			t.Fatalf("parsed but not written: %v", err)
		}
	})
}

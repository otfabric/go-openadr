// SPDX-License-Identifier: MIT

package discovery

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
)

// The part of the DNS message format (RFC 1035) that DNS-SD over
// Multicast DNS (RFC 6762, RFC 6763) needs.

const (
	typeA    = 1
	typePTR  = 12
	typeTXT  = 16
	typeAAAA = 28
	typeSRV  = 33
	typeANY  = 255

	classIN = 1
	// The top bit of the class: in a question, "answer me alone"; in a
	// record, "this replaces what you knew".
	classTop = 0x8000

	flagResponse      = 0x8000
	flagAuthoritative = 0x0400

	maxMessage = 9000
)

// name is a domain name as its labels, without the root. A label of an
// instance name may hold dots and spaces, which is why it is not a string.
type name []string

func (n name) String() string { return strings.Join(n, ".") + "." }

func (n name) equal(o name) bool {
	if len(n) != len(o) {
		return false
	}
	for i := range n {
		if !strings.EqualFold(n[i], o[i]) {
			return false
		}
	}
	return true
}

type question struct {
	name  name
	qtype uint16
	// unicast is the QU bit: the answer is wanted by unicast.
	unicast bool
}

// record is a resource record of one of the types above. Which of the
// fields below ttl are set depends on rtype.
type record struct {
	name  name
	rtype uint16
	// unique sets the cache-flush bit: the record is the only one of its
	// name and type.
	unique bool
	ttl    uint32

	target name       // PTR, SRV
	port   uint16     // SRV
	text   []string   // TXT
	addr   netip.Addr // A, AAAA
}

type message struct {
	id        uint16
	response  bool
	questions []question
	// answers holds the answer and the additional section alike: a
	// browser has the same use for both.
	answers    []record
	additional []record
}

var errMalformed = errors.New("discovery: malformed DNS message")

func appendName(b []byte, n name) ([]byte, error) {
	total := 1
	for _, label := range n {
		size := len(label)
		if size == 0 || size > 63 {
			return nil, errors.New("discovery: a label of a name is empty or longer than 63 bytes")
		}
		total += 1 + size
		b = append(b, byte(size))
		b = append(b, label...)
	}
	if total > 255 {
		return nil, errors.New("discovery: a name is longer than 255 bytes")
	}
	return append(b, 0), nil
}

func (m message) marshal() ([]byte, error) {
	var flags uint16
	if m.response {
		flags = flagResponse | flagAuthoritative
	}
	b := make([]byte, 0, 512)
	b = binary.BigEndian.AppendUint16(b, m.id)
	b = binary.BigEndian.AppendUint16(b, flags)
	for _, count := range []int{len(m.questions), len(m.answers), 0, len(m.additional)} {
		if count < 0 || count > 0xFFFF {
			return nil, errors.New("discovery: the message is too large")
		}
		b = binary.BigEndian.AppendUint16(b, uint16(count))
	}
	var err error
	for _, q := range m.questions {
		if b, err = appendName(b, q.name); err != nil {
			return nil, err
		}
		class := uint16(classIN)
		if q.unicast {
			class |= classTop
		}
		b = binary.BigEndian.AppendUint16(b, q.qtype)
		b = binary.BigEndian.AppendUint16(b, class)
	}
	for _, list := range [][]record{m.answers, m.additional} {
		for _, r := range list {
			if b, err = appendRecord(b, r); err != nil {
				return nil, err
			}
		}
	}
	if len(b) > maxMessage {
		return nil, errors.New("discovery: the message is too large")
	}
	return b, nil
}

func appendRecord(b []byte, r record) ([]byte, error) {
	b, err := appendName(b, r.name)
	if err != nil {
		return nil, err
	}
	class := uint16(classIN)
	if r.unique {
		class |= classTop
	}
	b = binary.BigEndian.AppendUint16(b, r.rtype)
	b = binary.BigEndian.AppendUint16(b, class)
	b = binary.BigEndian.AppendUint32(b, r.ttl)
	lengthAt := len(b)
	b = append(b, 0, 0)
	switch r.rtype {
	case typePTR:
		b, err = appendName(b, r.target)
	case typeSRV:
		b = append(b, 0, 0, 0, 0) // priority and weight
		b = binary.BigEndian.AppendUint16(b, r.port)
		b, err = appendName(b, r.target)
	case typeTXT:
		if len(r.text) == 0 {
			b = append(b, 0)
		}
		for _, s := range r.text {
			size := len(s)
			if size > 255 {
				return nil, errors.New("discovery: a TXT entry is longer than 255 bytes")
			}
			b = append(b, byte(size))
			b = append(b, s...)
		}
	case typeA:
		if !r.addr.Is4() {
			return nil, errors.New("discovery: an A record needs an IPv4 address")
		}
		a := r.addr.As4()
		b = append(b, a[:]...)
	case typeAAAA:
		if !r.addr.Is6() || r.addr.Is4In6() {
			return nil, errors.New("discovery: an AAAA record needs an IPv6 address")
		}
		a := r.addr.As16()
		b = append(b, a[:]...)
	default:
		return nil, errors.New("discovery: a record of a type that is not written")
	}
	if err != nil {
		return nil, err
	}
	size := len(b) - lengthAt - 2
	if size < 0 || size > 0xFFFF {
		return nil, errors.New("discovery: the data of a record is longer than 65535 bytes")
	}
	binary.BigEndian.PutUint16(b[lengthAt:], uint16(size))
	return b, nil
}

// walkName follows the name at off, compression pointers included, and
// calls label, when not nil, for each of its labels. It returns the offset
// after the name where it was first read, the number of labels and the
// length of the name as written uncompressed, the root octet included.
//
// A pointer has to point before the labels that led to it (RFC 1035,
// section 4.1.4: to a prior occurrence), so a name ends after at most as
// many pointers as the message has bytes.
func walkName(msg []byte, off int, label func([]byte)) (after, labels, size int, err error) {
	after, size = -1, 1
	for start := off; ; {
		if off >= len(msg) {
			return 0, 0, 0, errMalformed
		}
		c := int(msg[off])
		switch {
		case c == 0:
			if after < 0 {
				after = off + 1
			}
			return after, labels, size, nil
		case c&0xC0 == 0xC0:
			if off+1 >= len(msg) {
				return 0, 0, 0, errMalformed
			}
			if after < 0 {
				after = off + 2
			}
			to := (c&0x3F)<<8 | int(msg[off+1])
			if to >= start {
				return 0, 0, 0, errMalformed
			}
			start, off = to, to
		case c&0xC0 != 0:
			return 0, 0, 0, errMalformed
		default:
			if off+1+c > len(msg) {
				return 0, 0, 0, errMalformed
			}
			if size += 1 + c; size > 255 {
				return 0, 0, 0, errMalformed
			}
			if label != nil {
				label(msg[off+1 : off+1+c])
			}
			labels++
			off += 1 + c
		}
	}
}

// readName reads the name at off and returns the offset after the name
// where it was first read. budget is what remains of the bytes the names
// of the message may take uncompressed; the name is charged to it, so that
// pointers cannot make a small message into many large names.
func readName(msg []byte, off int, budget *int) (name, int, error) {
	after, labels, size, err := walkName(msg, off, nil)
	if err != nil {
		return nil, 0, err
	}
	if *budget -= size; *budget < 0 {
		return nil, 0, errMalformed
	}
	n := make(name, 0, labels)
	_, _, _, _ = walkName(msg, off, func(l []byte) { n = append(n, string(l)) })
	return n, after, nil
}

// nameBudget is how many bytes the names of a message may take
// uncompressed, in all: a few times the message, which compression of
// ordinary records stays well within, but not the hundreds of times that
// pointers to one long name could make of it.
func nameBudget(msg []byte) int { return 4*len(msg) + 1024 }

func parseMessage(msg []byte) (message, error) {
	if len(msg) < 12 || len(msg) > maxMessage {
		return message{}, errMalformed
	}
	m := message{id: binary.BigEndian.Uint16(msg), response: msg[2]&0x80 != 0}
	// An opcode other than a standard query is not for us.
	if msg[2]&0x78 != 0 {
		return message{}, errMalformed
	}
	counts := [4]int{}
	for i := range counts {
		counts[i] = int(binary.BigEndian.Uint16(msg[4+2*i:]))
	}
	off := 12
	budget := nameBudget(msg)
	var err error
	for range counts[0] {
		var q question
		if q.name, off, err = readName(msg, off, &budget); err != nil {
			return message{}, err
		}
		if off+4 > len(msg) {
			return message{}, errMalformed
		}
		q.qtype = binary.BigEndian.Uint16(msg[off:])
		class := binary.BigEndian.Uint16(msg[off+2:])
		q.unicast = class&classTop != 0
		off += 4
		if class&^classTop == classIN || class&^classTop == typeANY {
			m.questions = append(m.questions, q)
		}
	}
	for section := 1; section < 4; section++ {
		for range counts[section] {
			var r record
			if r.name, off, err = readName(msg, off, &budget); err != nil {
				return message{}, err
			}
			if off+10 > len(msg) {
				return message{}, errMalformed
			}
			r.rtype = binary.BigEndian.Uint16(msg[off:])
			class := binary.BigEndian.Uint16(msg[off+2:])
			r.unique = class&classTop != 0
			r.ttl = binary.BigEndian.Uint32(msg[off+4:])
			length := int(binary.BigEndian.Uint16(msg[off+8:]))
			off += 10
			end := off + length
			if end > len(msg) {
				return message{}, errMalformed
			}
			known, err := parseData(msg, off, end, &r, &budget)
			if err != nil {
				return message{}, err
			}
			off = end
			if !known || class&^classTop != classIN || section == 2 {
				continue
			}
			if section == 1 {
				m.answers = append(m.answers, r)
			} else {
				m.additional = append(m.additional, r)
			}
		}
	}
	return m, nil
}

// parseData reads the data of a record, which lies in msg[off:end], and
// reports whether the record is of a type that is understood.
func parseData(msg []byte, off, end int, r *record, budget *int) (bool, error) {
	var err error
	switch r.rtype {
	case typePTR:
		r.target, _, err = readName(msg[:end], off, budget)
	case typeSRV:
		if end-off < 7 {
			return false, errMalformed
		}
		r.port = binary.BigEndian.Uint16(msg[off+4:])
		r.target, _, err = readName(msg[:end], off+6, budget)
	case typeTXT:
		for off < end {
			n := int(msg[off])
			if off+1+n > end {
				return false, errMalformed
			}
			r.text = append(r.text, string(msg[off+1:off+1+n]))
			off += 1 + n
		}
	case typeA:
		if end-off != 4 {
			return false, errMalformed
		}
		r.addr = netip.AddrFrom4([4]byte(msg[off:end]))
	case typeAAAA:
		if end-off != 16 {
			return false, errMalformed
		}
		r.addr = netip.AddrFrom16([16]byte(msg[off:end]))
	default:
		return false, nil
	}
	return err == nil, err
}

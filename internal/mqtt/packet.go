// SPDX-License-Identifier: MIT

// Package mqtt is a client for MQTT 3.1.1 (OASIS standard, also ISO/IEC
// 20922), with what the MQTT binding of OpenADR notifications needs:
// publishing and subscribing at QoS 1 over TCP or TLS, with a clean
// session. It is internal: the public API is package notification/mqtt.
package mqtt

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Control packet types.
const (
	TypeConnect     = 1
	TypeConnack     = 2
	TypePublish     = 3
	TypePuback      = 4
	TypeSubscribe   = 8
	TypeSuback      = 9
	TypeUnsubscribe = 10
	TypeUnsuback    = 11
	TypePingreq     = 12
	TypePingresp    = 13
	TypeDisconnect  = 14
)

// MaxPacket bounds the packets this client reads: far above a notification,
// far below what the protocol allows (256 MiB).
const MaxPacket = 4 << 20

// ErrMalformed reports a packet that violates the protocol.
var ErrMalformed = errors.New("mqtt: malformed packet")

// Packet is a control packet: its type and flags, and what follows the
// fixed header.
type Packet struct {
	Type  byte
	Flags byte
	Body  []byte
}

// Encode returns the bytes of the packet on the wire.
func (p Packet) Encode() []byte {
	out := []byte{p.Type<<4 | p.Flags&0x0f}
	n := len(p.Body)
	for {
		b := byte(n % 128)
		if n /= 128; n > 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			break
		}
	}
	return append(out, p.Body...)
}

// ReadPacket reads one control packet.
func ReadPacket(r io.Reader) (Packet, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return Packet{}, err
	}
	length, shift := 0, 0
	for {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return Packet{}, unexpected(err)
		}
		length |= int(b[0]&0x7f) << shift
		if b[0]&0x80 == 0 {
			break
		}
		if shift += 7; shift > 21 {
			return Packet{}, fmt.Errorf("%w: remaining length of more than four bytes", ErrMalformed)
		}
	}
	if length > MaxPacket {
		return Packet{}, fmt.Errorf("%w: a packet of %d bytes", ErrMalformed, length)
	}
	p := Packet{Type: first[0] >> 4, Flags: first[0] & 0x0f, Body: make([]byte, length)}
	if _, err := io.ReadFull(r, p.Body); err != nil {
		return Packet{}, unexpected(err)
	}
	return p, nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// AppendString appends a UTF-8 string with its two-byte length.
func AppendString(b []byte, s string) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
	return append(b, s...)
}

// String takes a length-prefixed string off the front of b.
func String(b []byte) (s string, rest []byte, err error) {
	if len(b) < 2 {
		return "", nil, ErrMalformed
	}
	n := int(binary.BigEndian.Uint16(b))
	if len(b) < 2+n {
		return "", nil, ErrMalformed
	}
	return string(b[2 : 2+n]), b[2+n:], nil
}

// Uint16 takes a two-byte number off the front of b.
func Uint16(b []byte) (v uint16, rest []byte, err error) {
	if len(b) < 2 {
		return 0, nil, ErrMalformed
	}
	return binary.BigEndian.Uint16(b), b[2:], nil
}

// Connect builds a CONNECT packet for a clean session.
func Connect(clientID, username, password string, keepAliveSeconds uint16) Packet {
	flags := byte(0x02) // clean session
	if username != "" {
		flags |= 0x80
		if password != "" {
			flags |= 0x40
		}
	}
	body := AppendString(nil, "MQTT")
	body = append(body, 4, flags) // protocol level 4 is MQTT 3.1.1
	body = binary.BigEndian.AppendUint16(body, keepAliveSeconds)
	body = AppendString(body, clientID)
	if username != "" {
		body = AppendString(body, username)
		if password != "" {
			body = AppendString(body, password)
		}
	}
	return Packet{Type: TypeConnect, Body: body}
}

// Publish builds a PUBLISH packet. id is the packet identifier and is used
// when qos is 1.
func Publish(topic string, payload []byte, qos byte, id uint16, dup bool) Packet {
	body := AppendString(nil, topic)
	flags := qos << 1
	if qos > 0 {
		body = binary.BigEndian.AppendUint16(body, id)
		if dup {
			flags |= 0x08
		}
	}
	return Packet{Type: TypePublish, Flags: flags, Body: append(body, payload...)}
}

// ParsePublish takes a PUBLISH packet apart.
func ParsePublish(p Packet) (topic string, payload []byte, qos byte, id uint16, err error) {
	qos = p.Flags >> 1 & 0x03
	if qos > 2 {
		return "", nil, 0, 0, ErrMalformed
	}
	topic, rest, err := String(p.Body)
	if err != nil {
		return "", nil, 0, 0, err
	}
	if qos > 0 {
		if id, rest, err = Uint16(rest); err != nil {
			return "", nil, 0, 0, err
		}
	}
	return topic, rest, qos, id, nil
}

// Subscribe builds a SUBSCRIBE packet for the filters, each at QoS 1.
func Subscribe(id uint16, filters []string) Packet {
	body := binary.BigEndian.AppendUint16(nil, id)
	for _, f := range filters {
		body = append(AppendString(body, f), 1)
	}
	return Packet{Type: TypeSubscribe, Flags: 0x02, Body: body}
}

// Ack builds a PUBACK or the like: a packet that is a packet identifier.
func Ack(packetType byte, id uint16) Packet {
	return Packet{Type: packetType, Body: binary.BigEndian.AppendUint16(nil, id)}
}

// ValidTopic reports whether a topic name can be published to: not empty,
// no wildcard, no NUL.
func ValidTopic(topic string) bool {
	if topic == "" || len(topic) > 65535 {
		return false
	}
	for i := 0; i < len(topic); i++ {
		if c := topic[i]; c == '+' || c == '#' || c == 0 {
			return false
		}
	}
	return true
}

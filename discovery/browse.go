// SPDX-License-Identifier: MIT

package discovery

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// Options decide how Browse looks.
type Options struct {
	// Timeout is how long to wait for VTNs to answer; 3 seconds when zero.
	// The context given to Browse can end the wait earlier.
	Timeout time.Duration
	// First makes Browse return as soon as one VTN has answered, for a
	// site that has one.
	First bool

	net network
}

// What one Browse keeps of the answers, and asks again about: far more
// than a site has VTNs, and little enough that a sender on the link that
// announces names without end cannot grow it.
const (
	maxInstances = 64
	// maxAddrs is the most addresses kept of one host.
	maxAddrs = 8
	// maxOpen is the most questions asked again at a time, besides the
	// browse itself.
	maxOpen = 16
)

// found collects the records of the answers, of at most maxInstances
// instances.
type found struct {
	// By the lower case name of the instance.
	instances map[string]name
	srv       map[string]record
	txt       map[string][]string
	// By the lower case name of the host, for hosts that a SRV record
	// kept names.
	addrs map[string][]netip.Addr
}

func key(n name) string { return strings.ToLower(n.String()) }

// isInstance reports whether a name is that of an instance of the service:
// one label before the name of the service.
func isInstance(n name) bool { return len(n) == len(serviceName)+1 && n[1:].equal(serviceName) }

// add takes in the records of a message. The instances come first, then
// what is known of them, then the addresses of their hosts, so that one
// message can tell everything in any order.
func (f *found) add(lists ...[]record) {
	for _, list := range lists {
		for _, r := range list {
			if r.ttl == 0 || r.rtype != typePTR || !r.name.equal(serviceName) || !isInstance(r.target) {
				// A goodbye is of something that is leaving.
				continue
			}
			if k := key(r.target); f.instances[k] != nil || len(f.instances) < maxInstances {
				f.instances[k] = r.target
			}
		}
	}
	for _, list := range lists {
		for _, r := range list {
			if r.ttl == 0 || r.rtype != typeSRV && r.rtype != typeTXT {
				continue
			}
			k := key(r.name)
			if f.instances[k] == nil {
				continue
			}
			if r.rtype == typeSRV {
				f.srv[k] = r
			} else {
				f.txt[k] = r.text
			}
		}
	}
	hosts := map[string]bool{}
	for _, srv := range f.srv {
		hosts[key(srv.target)] = true
	}
	for _, list := range lists {
		for _, r := range list {
			if r.ttl == 0 || r.rtype != typeA && r.rtype != typeAAAA {
				continue
			}
			k := key(r.name)
			if hosts[k] && len(f.addrs[k]) < maxAddrs && !slices.Contains(f.addrs[k], r.addr) {
				f.addrs[k] = append(f.addrs[k], r.addr)
			}
		}
	}
	// A host that no SRV record names any more is forgotten.
	for k := range f.addrs {
		if !hosts[k] {
			delete(f.addrs, k)
		}
	}
}

// vtns returns the VTNs of which enough is known, and up to maxOpen of the
// questions that would complete the others.
func (f *found) vtns() ([]VTN, []question) {
	var (
		out  []VTN
		open []question
	)
	keys := make([]string, 0, len(f.instances))
	for k := range f.instances {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		instance := f.instances[k]
		srv, hasSRV := f.srv[k]
		text, hasTXT := f.txt[k]
		if !hasSRV && len(open) < maxOpen {
			open = append(open, question{name: instance, qtype: typeSRV})
		}
		if !hasTXT && len(open) < maxOpen {
			open = append(open, question{name: instance, qtype: typeTXT})
		}
		if !hasSRV || !hasTXT {
			continue
		}
		addrs := f.addrs[key(srv.target)]
		if len(addrs) == 0 && len(open) < maxOpen {
			// Wanted, not needed: a VEN is to use the name.
			open = append(open, question{name: srv.target, qtype: typeA})
		}
		out = append(out, vtnFrom(instance, srv, text, addrs))
	}
	return out, open
}

// Browse asks the local network for VTNs and returns those that answered
// within the timeout, by instance name. Finding none is not an error.
//
// It asks in both ways there are. As a one-shot resolver (RFC 6762,
// section 5.1), from a port of its own, to which a responder answers
// directly: that needs nothing but a socket, and works beside the resolver
// of the system. And, where the Multicast DNS port can be shared, as a
// Multicast DNS querier, from that port, hearing what is answered to the
// whole network: some responders, OpenLEADR's among them, answer no other
// way. A machine on which the port cannot be shared still finds the
// responders that answer directly.
func Browse(ctx context.Context, o Options) ([]VTN, error) {
	if o.Timeout <= 0 {
		o.Timeout = 3 * time.Second
	}
	network := o.net
	if network == nil {
		network = realNetwork{}
	}
	links, err := network.links()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	var (
		conns  []net.PacketConn
		on     []link
		failed error
	)
	for _, l := range links {
		conn, err := network.asker(l)
		if err != nil {
			failed = err
			continue
		}
		conns, on = append(conns, conn), append(on, l)
		// The Multicast DNS port as well, where it can be had.
		if member, err := network.member(l); err == nil {
			conns, on = append(conns, member), append(on, l)
		}
	}
	if len(conns) == 0 {
		return nil, fmt.Errorf("%w: cannot open a socket to ask from: %w", ErrNoNetwork, failed)
	}
	answers := make(chan message)
	var readers sync.WaitGroup
	for i, conn := range conns {
		readers.Go(func() {
			buf := make([]byte, maxMessage)
			for {
				n, from, err := conn.ReadFrom(buf)
				if err != nil {
					return
				}
				// A response comes from the Multicast DNS port (RFC 6762,
				// section 6) and from the link (section 11); the others
				// are not read.
				if udp, ok := from.(*net.UDPAddr); !ok || udp.Port != group.Port || !on[i].onLink(from) {
					continue
				}
				if m, err := parseMessage(buf[:n]); err == nil && m.response {
					select {
					case answers <- m:
					case <-ctx.Done():
						return
					}
				}
			}
		})
	}
	defer func() {
		// The readers wait to hand over an answer, or to read one.
		cancel()
		for _, conn := range conns {
			_ = conn.Close()
		}
		readers.Wait()
	}()

	ask := func(questions ...question) {
		// Small enough for one packet, in any number that occurs here.
		for len(questions) > 0 {
			n := min(len(questions), 8)
			if b, err := (message{questions: questions[:n]}).marshal(); err == nil {
				for _, conn := range conns {
					_, _ = conn.WriteTo(b, group)
				}
			}
			questions = questions[n:]
		}
	}
	f := &found{instances: map[string]name{}, srv: map[string]record{}, txt: map[string][]string{}, addrs: map[string][]netip.Addr{}}
	browse := question{name: serviceName, qtype: typePTR}
	ask(browse)
	// Packets get lost: ask again, less and less often.
	wait := 500 * time.Millisecond
	again := time.NewTimer(wait)
	defer again.Stop()
	for {
		select {
		case m := <-answers:
			f.add(m.answers, m.additional)
			if !o.First {
				continue
			}
			if vtns, open := f.vtns(); len(vtns) > 0 && len(open) == 0 {
				return vtns, nil
			}
		case <-again.C:
			_, open := f.vtns()
			ask(append([]question{browse}, open...)...)
			wait *= 2
			again.Reset(wait)
		case <-ctx.Done():
			vtns, _ := f.vtns()
			if len(vtns) == 0 && ctx.Err() == context.Canceled {
				return nil, ctx.Err()
			}
			return vtns, nil
		}
	}
}

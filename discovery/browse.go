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

// found collects the records of the answers.
type found struct {
	// By the lower case name of the instance.
	instances map[string]name
	srv       map[string]record
	txt       map[string][]string
	// By the lower case name of the host.
	addrs map[string][]netip.Addr
}

func key(n name) string { return strings.ToLower(n.String()) }

func (f *found) add(records []record) {
	for _, r := range records {
		if r.ttl == 0 {
			// A goodbye, of something that is leaving.
			continue
		}
		switch r.rtype {
		case typePTR:
			if r.name.equal(serviceName) && len(r.target) == len(serviceName)+1 {
				f.instances[key(r.target)] = r.target
			}
		case typeSRV:
			f.srv[key(r.name)] = r
		case typeTXT:
			f.txt[key(r.name)] = r.text
		case typeA, typeAAAA:
			k := key(r.name)
			if !slices.Contains(f.addrs[k], r.addr) {
				f.addrs[k] = append(f.addrs[k], r.addr)
			}
		}
	}
}

// vtns returns the VTNs of which enough is known, and the questions that
// would complete the others.
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
		if !hasSRV {
			open = append(open, question{name: instance, qtype: typeSRV})
		}
		if !hasTXT {
			open = append(open, question{name: instance, qtype: typeTXT})
		}
		if !hasSRV || !hasTXT {
			continue
		}
		addrs := f.addrs[key(srv.target)]
		if len(addrs) == 0 {
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
// It asks as a one-shot resolver does (RFC 6762, section 5.1), from a port
// of its own: it does not need the Multicast DNS port, and it works beside
// the resolver of the system.
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
		failed error
	)
	for _, l := range links {
		conn, err := network.asker(l)
		if err != nil {
			failed = err
			continue
		}
		conns = append(conns, conn)
	}
	if len(conns) == 0 {
		return nil, fmt.Errorf("discovery: cannot open a socket to ask from: %w", failed)
	}
	answers := make(chan message)
	var readers sync.WaitGroup
	for _, conn := range conns {
		readers.Add(1)
		go func() {
			defer readers.Done()
			buf := make([]byte, maxMessage)
			for {
				n, _, err := conn.ReadFrom(buf)
				if err != nil {
					return
				}
				if m, err := parseMessage(buf[:n]); err == nil && m.response {
					select {
					case answers <- m:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
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
			f.add(m.answers)
			f.add(m.additional)
			if vtns, open := f.vtns(); o.First && len(vtns) > 0 && len(open) == 0 {
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

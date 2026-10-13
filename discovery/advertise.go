// SPDX-License-Identifier: MIT

package discovery

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Advertisement is what a VTN announces on the local network.
type Advertisement struct {
	// Instance names the VTN to people, such as "Building 4 VTN". It has
	// to be unique on the network; "OpenADR 3 VTN" when empty.
	Instance string
	// Host is the host name to announce, without ".local"; the name of
	// the machine when empty.
	Host string
	// Port is the port the API is served at. Required.
	Port int
	// BasePath is what comes before the paths of the API, such as
	// "openadr3", when it is not served at the root.
	BasePath string
	// Version is the OpenADR version; "3.1.0" when empty.
	Version string
	// LocalURL is the URL of the API. When empty it is https, the host
	// with ".local", the port and the base path.
	LocalURL string
	// ProgramNames are the names of the programs meant for local VENs.
	ProgramNames []string
	// NoAuth announces a VTN that serves clients without a token (see
	// Config.AllowAnonymous of package server). The default announces one
	// that requires authentication.
	NoAuth bool
	// OpenAPIURL is where the VTN serves its OpenAPI document, if it does.
	OpenAPIURL string
	// Logger receives failures to send. Nil disables logging.
	Logger *slog.Logger

	net network
}

// Advertiser answers the queries of the local network for a VTN, until it
// is closed.
type Advertiser struct {
	instance, host name
	port           uint16
	text           []string
	logger         *slog.Logger

	ports []advertisedOn
	stop  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once
}

type advertisedOn struct {
	link link
	conn net.PacketConn
}

// The times to live of the records: short for what changes when the host
// moves, long for the rest, as RFC 6762 recommends.
const (
	ttlHost  = 120
	ttlOther = 4500
	// A resolver that is not Multicast DNS proper may keep an answer this long.
	ttlLegacy = 10
)

// Advertise starts announcing a VTN on every network interface that has
// multicast. It fails when there is none, or when port 5353 cannot be
// shared with the resolver of the system.
//
// It does not probe for the names first: an instance or host name that
// another machine on the network already uses is not detected.
func Advertise(a Advertisement) (*Advertiser, error) {
	if a.Port < 1 || a.Port > 65535 {
		return nil, errors.New("discovery: Advertisement.Port is required")
	}
	if a.Instance == "" {
		a.Instance = "OpenADR 3 VTN"
	}
	if err := checkLabel("Advertisement.Instance", a.Instance); err != nil {
		return nil, err
	}
	host := a.Host
	if host == "" {
		host, _ = os.Hostname()
	}
	host = hostLabel(strings.TrimSuffix(strings.TrimSuffix(host, "."), ".local"))
	if err := checkLabel("Advertisement.Host", host); err != nil {
		return nil, err
	}
	if a.Version == "" {
		a.Version = "3.1.0"
	}
	base := strings.Trim(a.BasePath, "/")
	if a.LocalURL == "" {
		a.LocalURL = "https://" + net.JoinHostPort(host+".local", strconv.Itoa(a.Port))
		if base != "" {
			a.LocalURL += "/" + base
		}
	}
	for _, p := range a.ProgramNames {
		if p == "" || strings.Contains(p, ",") {
			return nil, fmt.Errorf("discovery: the program name %q is empty or holds a comma", p)
		}
	}
	requiresAuth := "True"
	if a.NoAuth {
		requiresAuth = "False"
	}
	text := []string{"version=" + a.Version, "base_path=" + base, "local_url=" + a.LocalURL, "requires_auth=" + requiresAuth}
	if len(a.ProgramNames) > 0 {
		text = append(text, "program_names="+strings.Join(a.ProgramNames, ","))
	}
	if a.OpenAPIURL != "" {
		text = append(text, "openapi_url="+a.OpenAPIURL)
	}
	for _, entry := range text {
		if len(entry) > 255 {
			key, _, _ := strings.Cut(entry, "=")
			return nil, fmt.Errorf("discovery: %s does not fit the 255 bytes of a TXT entry", key)
		}
	}

	network := a.net
	if network == nil {
		network = realNetwork{}
	}
	links, err := network.links()
	if err != nil {
		return nil, err
	}
	ad := &Advertiser{
		instance: append(name{a.Instance}, serviceName...), host: name{host, "local"},
		port: uint16(a.Port), text: text, logger: a.Logger, stop: make(chan struct{}),
	}
	var failed error
	for _, l := range links {
		conn, err := network.member(l)
		if err != nil {
			failed = err
			continue
		}
		ad.ports = append(ad.ports, advertisedOn{link: l, conn: conn})
	}
	if len(ad.ports) == 0 {
		return nil, fmt.Errorf("discovery: cannot listen for Multicast DNS: %w", failed)
	}
	for _, p := range ad.ports {
		ad.wg.Add(1)
		go ad.serve(p)
	}
	// Announce, twice a second apart, so that those who already listen
	// need not ask.
	ad.wg.Add(1)
	go func() {
		defer ad.wg.Done()
		for i := 0; i < 2; i++ {
			for _, p := range ad.ports {
				ad.announce(p, false)
			}
			select {
			case <-ad.stop:
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return ad, nil
}

// Close takes the announcement back and stops answering.
func (ad *Advertiser) Close() error {
	ad.once.Do(func() {
		close(ad.stop)
		for _, p := range ad.ports {
			// Goodbye: the same records, with no time to live.
			ad.announce(p, true)
			_ = p.conn.Close()
		}
		ad.wg.Wait()
	})
	return nil
}

func (ad *Advertiser) warn(msg string, err error) {
	if ad.logger != nil {
		ad.logger.Warn(msg, slog.Any("error", err))
	}
}

// The records of the VTN on one link.

func (ad *Advertiser) ptr() record {
	return record{name: serviceName, rtype: typePTR, ttl: ttlOther, target: ad.instance}
}

func (ad *Advertiser) srv() record {
	return record{name: ad.instance, rtype: typeSRV, unique: true, ttl: ttlHost, target: ad.host, port: ad.port}
}

func (ad *Advertiser) txt() record {
	return record{name: ad.instance, rtype: typeTXT, unique: true, ttl: ttlOther, text: ad.text}
}

func (ad *Advertiser) addresses(l link, rtype uint16) []record {
	var out []record
	for _, addr := range l.addrs {
		r := record{name: ad.host, unique: true, ttl: ttlHost, addr: addr, rtype: typeAAAA}
		if addr.Is4() {
			r.rtype = typeA
		}
		if rtype == typeANY || rtype == r.rtype {
			out = append(out, r)
		}
	}
	return out
}

func (ad *Advertiser) announce(p advertisedOn, goodbye bool) {
	m := message{response: true, answers: []record{ad.ptr(), ad.srv(), ad.txt()}}
	m.answers = append(m.answers, ad.addresses(p.link, typeANY)...)
	if goodbye {
		for i := range m.answers {
			m.answers[i].ttl = 0
		}
	}
	ad.send(p, m, group)
}

func (ad *Advertiser) send(p advertisedOn, m message, to net.Addr) {
	b, err := m.marshal()
	if err == nil {
		_, err = p.conn.WriteTo(b, to)
	}
	if err != nil {
		select {
		case <-ad.stop:
		default:
			ad.warn("discovery: cannot send on "+p.link.name, err)
		}
	}
}

func (ad *Advertiser) serve(p advertisedOn) {
	defer ad.wg.Done()
	buf := make([]byte, maxMessage)
	for {
		n, from, err := p.conn.ReadFrom(buf)
		if err != nil {
			select {
			case <-ad.stop:
				return
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			ad.warn("discovery: cannot receive on "+p.link.name, err)
			return
		}
		// A socket may be handed what arrived on another interface: the
		// addresses of this one are of no use to who asked there.
		if !p.link.onLink(from) {
			continue
		}
		query, err := parseMessage(buf[:n])
		if err != nil || query.response {
			continue
		}
		ad.answer(p, query, from)
	}
}

// answer replies to the questions of a query that are about this VTN.
func (ad *Advertiser) answer(p advertisedOn, query message, from net.Addr) {
	reply := message{response: true}
	add := func(list *[]record, records ...record) {
		for _, r := range records {
			same := func(o record) bool { return o.rtype == r.rtype && o.name.equal(r.name) && o.addr == r.addr }
			if !slices.ContainsFunc(reply.answers, same) && !slices.ContainsFunc(reply.additional, same) {
				*list = append(*list, r)
			}
		}
	}
	wants := func(q question, rtype uint16) bool { return q.qtype == rtype || q.qtype == typeANY }
	unicast := false
	for _, q := range query.questions {
		switch {
		case q.name.equal(serviceName) && wants(q, typePTR):
			// Who says it knows us already is not told again.
			known := slices.ContainsFunc(query.answers, func(r record) bool {
				return r.rtype == typePTR && r.name.equal(serviceName) && r.target.equal(ad.instance)
			})
			if known {
				continue
			}
			add(&reply.answers, ad.ptr())
			add(&reply.additional, ad.srv(), ad.txt())
			add(&reply.additional, ad.addresses(p.link, typeANY)...)
		case q.name.equal(ad.instance):
			if wants(q, typeSRV) {
				add(&reply.answers, ad.srv())
				add(&reply.additional, ad.addresses(p.link, typeANY)...)
			}
			if wants(q, typeTXT) {
				add(&reply.answers, ad.txt())
			}
		case q.name.equal(ad.host):
			add(&reply.answers, ad.addresses(p.link, q.qtype)...)
		case q.name.equal(metaName) && wants(q, typePTR):
			add(&reply.answers, record{name: metaName, rtype: typePTR, ttl: ttlOther, target: serviceName})
		default:
			continue
		}
		unicast = unicast || q.unicast
	}
	if len(reply.answers) == 0 {
		return
	}
	to := net.Addr(group)
	if udp, ok := from.(*net.UDPAddr); ok && udp.Port != group.Port {
		// A resolver that is not Multicast DNS proper: it is answered as
		// an ordinary DNS server would, directly, with its ID and its
		// questions, and without the bits and the long lives it would
		// not understand.
		to, reply.id, reply.questions = from, query.id, query.questions
		for _, list := range [][]record{reply.answers, reply.additional} {
			for i := range list {
				list[i].unique = false
				list[i].ttl = min(list[i].ttl, ttlLegacy)
			}
		}
		for i := range reply.questions {
			reply.questions[i].unicast = false
		}
	} else if unicast {
		to = from
	}
	ad.send(p, reply, to)
}

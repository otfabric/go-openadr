// SPDX-License-Identifier: MIT

// Package discovery finds VTNs on the local network, and lets a VTN be
// found, with DNS-SD over Multicast DNS as OpenADR 3.1.0 describes it
// ("Discovery and Configuration of Local VTNs"): the service type is
// _openadr3._tcp, and a TXT record carries the version, the base path and
// URL of the API, the program names and whether the VTN authenticates.
//
// A VEN at a customer site looks for the gateway there:
//
//	vtns, err := discovery.Browse(ctx, discovery.Options{})
//	for _, vtn := range vtns {
//		fmt.Println(vtn.Instance, vtn.URL(), vtn.RequiresAuth)
//	}
//
// and a VTN announces itself for as long as it runs:
//
//	ad, err := discovery.Advertise(discovery.Advertisement{Port: 8443, BasePath: "openadr3"})
//	defer ad.Close()
//
// Multicast DNS tells nothing about who answers: anyone on the link can
// claim to be a VTN. What a VEN finds this way is an address to try, and
// the TLS certificate of the VTN, and the VEN's credentials, are what make
// it trustworthy, as for an address from any other source.
//
// The package speaks Multicast DNS over IPv4. It announces the IPv6
// addresses of the host as well, and reports those of the VTNs it finds.
package discovery

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Service is the DNS-SD service type of an OpenADR 3 VTN.
const Service = "_openadr3._tcp"

var (
	serviceName = name{"_openadr3", "_tcp", "local"}
	// metaName is the name a browser of all service types asks for.
	metaName = name{"_services", "_dns-sd", "_udp", "local"}
	group    = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
)

// VTN is a VTN as it announces itself.
type VTN struct {
	// Instance is the name of the service instance, such as "My VTN".
	Instance string
	// Host is the host name of the VTN, such as "myvtn.local", and Port
	// the port of its API.
	Host string
	Port int
	// Addrs are the addresses the VTN announced for Host. The
	// specification asks a VEN to prefer the name, because addresses
	// change; they are for a system that cannot resolve .local names.
	Addrs []netip.Addr
	// Version is the OpenADR version, such as "3.1.0".
	Version string
	// BasePath is what comes before the paths of the API, such as
	// "openadr3/3.1.0".
	BasePath string
	// LocalURL is the URL of the API as the VTN gives it.
	LocalURL string
	// ProgramNames are the programs meant for local VENs.
	ProgramNames []string
	// RequiresAuth says the VTN wants a token. It is false only for a VTN
	// that says so.
	RequiresAuth bool
	// OpenAPIURL is where the VTN serves its OpenAPI document.
	OpenAPIURL string
	// Text holds every key of the TXT record, in lower case, including
	// those above and any the specification does not name.
	Text map[string]string
}

// URL returns the base URL of the VTN's API: LocalURL when the VTN gave
// one, and otherwise https, the host, the port and the base path.
func (v VTN) URL() string {
	if v.LocalURL != "" {
		return v.LocalURL
	}
	u := "https://" + net.JoinHostPort(v.Host, strconv.Itoa(v.Port))
	if path := strings.Trim(v.BasePath, "/"); path != "" {
		u += "/" + path
	}
	return u
}

// vtnFrom builds a VTN from the records of one instance.
func vtnFrom(instance name, srv record, text []string, addrs []netip.Addr) VTN {
	v := VTN{
		Instance: instance[0], Host: strings.TrimSuffix(srv.target.String(), "."), Port: int(srv.port),
		Addrs: slices.Clone(addrs), RequiresAuth: true, Text: map[string]string{},
	}
	slices.SortFunc(v.Addrs, func(a, b netip.Addr) int { return a.Compare(b) })
	for _, entry := range text {
		key, value, _ := strings.Cut(entry, "=")
		key = strings.ToLower(key)
		if _, seen := v.Text[key]; key == "" || seen {
			// DNS-SD: the first of several wins.
			continue
		}
		v.Text[key] = value
	}
	v.Version, v.BasePath, v.LocalURL, v.OpenAPIURL = v.Text["version"], v.Text["base_path"], v.Text["local_url"], v.Text["openapi_url"]
	for _, p := range strings.Split(v.Text["program_names"], ",") {
		if p = strings.TrimSpace(p); p != "" {
			v.ProgramNames = append(v.ProgramNames, p)
		}
	}
	if auth, ok := v.Text["requires_auth"]; ok && strings.EqualFold(auth, "false") {
		v.RequiresAuth = false
	}
	return v
}

// link is one network interface on which Multicast DNS is spoken.
type link struct {
	name string
	// addrs are the addresses of the host on the interface, and nets the
	// networks they lie in: a query from elsewhere is not for this link.
	addrs []netip.Addr
	nets  []netip.Prefix
	// ip4 is the address multicast packets are sent from.
	ip4 net.IP
	// ifi is the interface itself, for the real network.
	ifi *net.Interface
}

func (l link) onLink(addr net.Addr) bool {
	udp, ok := addr.(*net.UDPAddr)
	if !ok {
		return false
	}
	ip, ok := netip.AddrFromSlice(udp.IP)
	if !ok {
		return false
	}
	ip = ip.Unmap()
	return slices.ContainsFunc(l.nets, func(p netip.Prefix) bool { return p.Contains(ip) })
}

// network opens the sockets of Multicast DNS. The one below is the real
// thing; tests have another.
type network interface {
	links() ([]link, error)
	// member opens a socket that receives what is sent to the group on
	// the link, at port 5353.
	member(l link) (net.PacketConn, error)
	// asker opens a socket at a port of its own, which sends to the group
	// on the link and receives the answers meant for it alone.
	asker(l link) (net.PacketConn, error)
}

type realNetwork struct{}

func (realNetwork) links() ([]link, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []link
	for i := range ifaces {
		ifi := &ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		l := link{name: ifi.Name, ifi: ifi}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			prefix, err := netip.ParsePrefix(ipnet.String())
			if err != nil {
				continue
			}
			ip := prefix.Addr().Unmap()
			switch {
			case ip.Is4():
				if l.ip4 == nil {
					l.ip4 = ip.AsSlice()
				}
				l.nets = append(l.nets, prefix.Masked())
			case ip.IsLinkLocalUnicast() || ip.IsLoopback():
				// Of no use to another host without a zone.
				continue
			}
			l.addrs = append(l.addrs, ip)
		}
		if l.ip4 != nil {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("discovery: no network interface with multicast and an IPv4 address")
	}
	return out, nil
}

func (realNetwork) member(l link) (net.PacketConn, error) {
	conn, err := net.ListenMulticastUDP("udp4", l.ifi, group)
	if err != nil {
		return nil, err
	}
	multicastOptions(conn, l.ip4)
	return conn, nil
}

func (realNetwork) asker(l link) (net.PacketConn, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: l.ip4})
	if err != nil {
		return nil, err
	}
	multicastOptions(conn, l.ip4)
	return conn, nil
}

// hostLabel makes a DNS label of a host name: its first label, with what
// a host name may not hold replaced.
func hostLabel(host string) string {
	host, _, _ = strings.Cut(host, ".")
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	label := strings.Trim(b.String(), "-")
	if len(label) > 63 {
		label = label[:63]
	}
	return label
}

func checkLabel(what, label string) error {
	if label == "" || len(label) > 63 {
		return fmt.Errorf("discovery: %s must be 1 to 63 bytes", what)
	}
	return nil
}

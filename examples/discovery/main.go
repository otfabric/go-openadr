// SPDX-License-Identifier: MIT

// Discovery shows how a VEN at a customer site finds the VTN there: the
// VTN announces itself on the local network with DNS-SD over Multicast
// DNS, as service type _openadr3._tcp, and the VEN asks the network.
//
// Multicast is not to be had everywhere, containers and some virtual
// networks among them. Where it is not, this example says so and ends.
//
// What is found is an address to try and nothing more: anyone on the
// network can announce a VTN. The TLS certificate of the VTN and the
// credentials of the VEN are what make it the right one.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-openadr/discovery"
)

func main() {
	// A gateway announces the VTN it serves at port 8443, under /openadr3.
	// The same can be seen with other tools: dns-sd -B _openadr3._tcp, or
	// avahi-browse -r _openadr3._tcp.
	ad, err := discovery.Advertise(discovery.Advertisement{
		Instance:     "Example gateway",
		Port:         8443,
		BasePath:     "openadr3",
		ProgramNames: []string{"local-flex"},
	})
	if err != nil {
		fmt.Println("cannot announce a VTN on this machine:", err)
		return
	}
	defer func() { _ = ad.Close() }()

	// A VEN looks for VTNs for two seconds. With First it would return at
	// the first answer, which is right for a site with one gateway.
	vtns, err := discovery.Browse(context.Background(), discovery.Options{Timeout: 2 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	if len(vtns) == 0 {
		fmt.Println("no VTN answered: multicast does not reach this machine's own sockets")
		return
	}
	for _, vtn := range vtns {
		fmt.Printf("found %q\n  API       %s\n  OpenADR   %s\n  programs  %v\n  token     %v\n  addresses %v\n",
			vtn.Instance, vtn.URL(), vtn.Version, vtn.ProgramNames, vtn.RequiresAuth, vtn.Addrs)
		// From here on it is an ordinary VTN:
		//   client.NewVEN(client.Config{BaseURL: vtn.URL(), Credentials: ...})
		// and without credentials when RequiresAuth is false.
	}
}

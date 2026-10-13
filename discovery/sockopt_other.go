// SPDX-License-Identifier: MIT

//go:build !unix && !windows

package discovery

import "net"

// multicastOptions does nothing where the socket options are not at hand:
// packets leave on the default interface, and are not seen on this host.
func multicastOptions(*net.UDPConn, net.IP) {}

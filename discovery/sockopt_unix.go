// SPDX-License-Identifier: MIT

//go:build unix

package discovery

import (
	"net"
	"syscall"
)

// multicastOptions makes a socket send its multicast packets on the given
// interface address, and lets other sockets of this host receive them:
// the standard library turns that off for a multicast listener, and a VTN
// and a VEN on one machine would not find each other.
func multicastOptions(conn *net.UDPConn, via net.IP) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1)
		if ip4 := via.To4(); ip4 != nil {
			_ = syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, [4]byte(ip4))
		}
	})
}

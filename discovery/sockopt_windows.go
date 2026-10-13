// SPDX-License-Identifier: MIT

//go:build windows

package discovery

import (
	"net"
	"syscall"
)

// multicastOptions: see the Unix variant.
func multicastOptions(conn *net.UDPConn, via net.IP) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1)
		if ip4 := via.To4(); ip4 != nil {
			_ = syscall.SetsockoptInet4Addr(syscall.Handle(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, [4]byte(ip4))
		}
	})
}

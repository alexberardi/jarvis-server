package sip

import (
	"net"
	"testing"
)

// otherLoopback is a second loopback address, the "someone else on the network" the scanner and
// registrar tests send from. Linux answers on all of 127.0.0.0/8; macOS has only 127.0.0.1 on lo0
// unless an alias is added, so there the tests that need it skip (CI's Linux jobs run them).
func otherLoopback(t *testing.T) string {
	t.Helper()
	const addr = "127.0.0.66"
	c, err := net.ListenPacket("udp", addr+":0")
	if err != nil {
		t.Skipf("this host can't use %s (%v); on macOS add it with `sudo ifconfig lo0 alias %s` to run this test", addr, err, addr)
	}
	c.Close()
	return addr
}

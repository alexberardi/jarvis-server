package cc

import (
	"net"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

func TestAddrKey(t *testing.T) {
	local := []net.IP{net.ParseIP("192.168.1.10"), net.ParseIP("fe80::1")}
	for _, c := range []struct{ in, want string }{
		{"127.0.0.1", sameHostKey},
		{"::1", sameHostKey},
		{"127.0.0.2", sameHostKey},
		{"192.168.1.10", sameHostKey}, // the server's own LAN address: same machine
		{"::ffff:192.168.1.10", sameHostKey},
		{"192.168.1.20", "192.168.1.20"},
		{"::ffff:192.168.1.20", "192.168.1.20"},
		{"", ""},
		{"not-an-ip", ""},
	} {
		if got := addrKey(c.in, local); got != c.want {
			t.Errorf("addrKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestThisDeviceMatch(t *testing.T) {
	local := []net.IP{net.ParseIP("192.168.1.10")}
	row := func(id, addr string) *nodeRow {
		n := &nodeRow{nodeID: id}
		if addr != "" {
			n.lastAddr.String, n.lastAddr.Valid = addr, true
		}
		return n
	}
	server, laptop, desk := row("server", "127.0.0.1"), row("laptop", "192.168.1.20"), row("desk", "192.168.1.30")
	for _, c := range []struct {
		name   string
		peer   string
		agents []*nodeRow
		want   string
		why    thisDeviceReason
	}{
		{"no agents", "192.168.1.20", nil, "", reasonNoAgents},
		{"one match", "192.168.1.20", []*nodeRow{server, laptop, desk}, "laptop", ""},
		{"loopback page, loopback agent", "127.0.0.1", []*nodeRow{server, laptop}, "server", ""},
		{"LAN page on the server, loopback agent", "192.168.1.10", []*nodeRow{server, laptop}, "server", ""},
		{"no match", "192.168.1.99", []*nodeRow{server, laptop}, "", reasonNoMatch},
		{"two agents behind one address", "192.168.1.20", []*nodeRow{laptop, row("vm", "192.168.1.20")}, "", reasonAmbiguous},
		{"no address yet", "192.168.1.20", []*nodeRow{row("new", "")}, "", reasonNoMatch},
		{"unparsable peer", "", []*nodeRow{laptop}, "", reasonNoMatch},
	} {
		n, why := thisDeviceMatch(c.peer, c.agents, local)
		got := ""
		if n != nil {
			got = n.nodeID
		}
		if got != c.want || why != c.why {
			t.Errorf("%s: got %q %q, want %q %q", c.name, got, why, c.want, c.why)
		}
	}
}

func TestThisDeviceRoute(t *testing.T) {
	e := newEnv(t)
	e.m.localIPs = func() []net.IP { return nil }
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.auth.addUser(1, "hh3", authn.RoleMember)
	outsider := e.auth.addUser(2, "hh2", authn.RoleMember)
	e.do("GET", "/api/v0/nodes/this-device", nil, nil).want(401)

	// No agent yet.
	if r := e.do("GET", "/api/v0/nodes/this-device", nil, bearer(owner)).want(200).json(); r["node_id"] != nil || r["reason"] != "no_agents" {
		t.Fatal(r)
	}
	// The agent heartbeats from this test's loopback address, as the page asks from it.
	n := e.pairDesktop(owner, "hh1")
	r := e.do("GET", "/api/v0/nodes/this-device", nil, bearer(owner)).want(200).json()
	if r["node_id"] != n.id || r["household_id"] != "hh1" || r["online"] != true {
		t.Fatalf("got %v", r)
	}
	if r := e.do("GET", "/api/v0/nodes/this-device?household_id=hh1", nil, bearer(owner)).want(200).json(); r["node_id"] != n.id {
		t.Fatal(r)
	}
	// Scoped to the asked household, and to the caller's own households.
	if r := e.do("GET", "/api/v0/nodes/this-device?household_id=hh3", nil, bearer(owner)).want(200).json(); r["node_id"] != nil {
		t.Fatal(r)
	}
	if r := e.do("GET", "/api/v0/nodes/this-device", nil, bearer(outsider)).want(200).json(); r["node_id"] != nil || r["reason"] != "no_agents" {
		t.Fatal(r)
	}
	e.do("GET", "/api/v0/nodes/this-device?household_id=hh1", nil, bearer(outsider)).detail(403, "User is not a member of this household")

	// G11: a forwarded header from a loopback peer is a proxy, never a match.
	h := bearer(owner)
	h["X-Forwarded-For"] = "192.168.1.20"
	if r := e.do("GET", "/api/v0/nodes/this-device", nil, h).want(200).json(); r["node_id"] != nil || r["reason"] != "proxied" {
		t.Fatal(r)
	}
	// A Pi in the same place is not "this computer".
	pi := e.createNode("pi1", "hh1")
	e.do("POST", "/api/v0/admin/nodes/heartbeat", nil, pi.h()).want(200)
	if r := e.do("GET", "/api/v0/nodes/this-device", nil, bearer(owner)).want(200).json(); r["node_id"] != n.id {
		t.Fatalf("a Pi changed the answer: %v", r)
	}
	// Two agents at one address: can't tell.
	e.pairDesktop(owner, "hh1")
	if r := e.do("GET", "/api/v0/nodes/this-device", nil, bearer(owner)).want(200).json(); r["reason"] != "ambiguous" {
		t.Fatal(r)
	}
}

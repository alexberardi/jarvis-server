package cc

import (
	"net"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// "This computer" (install plan §3.2, Q2 (a), decided; webapp 01 §3.3 step 1).
//
// GET /nodes/this-device compares the TCP peer address of the page's request with the address
// of each of the caller's desktop agents' latest heartbeat (cc_nodes.last_addr). Exactly one
// match names that agent; anything else is "can't tell".
//
// IT IS A HINT AND GRANTS NOTHING. It only picks which of the caller's own agents the web app
// shows as "This computer" and uses as chat's default node. Every node it can name is already in
// the caller's households, and every action on it is checked again by the route that performs
// it. A wrong answer changes a default, nothing more.
//
// G11: only the socket's peer address counts, never X-Forwarded-For or any other header a
// client can write. Through a reverse proxy or a tunnel the peer is the proxy, which says
// nothing about the browser's computer, so a request that arrives from a loopback peer carrying
// forwarding headers is "can't tell" (the headers can only take a match away, never make one).
//
// Loopback and this server's own addresses are one place, "this server": an agent on the
// server's machine heartbeats from 127.0.0.1 or the LAN address it used, and a browser on that
// machine may open the page by either.

type thisDeviceReason string

const (
	reasonNoAgents  thisDeviceReason = "no_agents" // the caller has no desktop agent with an address
	reasonNoMatch   thisDeviceReason = "no_match"  // none of them heartbeats from this address
	reasonAmbiguous thisDeviceReason = "ambiguous" // several do (NAT, VMs, a shared address)
	reasonProxied   thisDeviceReason = "proxied"   // the request came through a proxy or tunnel
)

// forwardHeaders mark a request relayed by a proxy or tunnel.
var forwardHeaders = []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip", "Cf-Connecting-Ip"}

// sameHostKey is the place every loopback and local-interface address maps to.
const sameHostKey = "this-server"

// localAddrs lists this server's own interface addresses (tests replace it via m.localIPs).
func localAddrs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			out = append(out, ipn.IP)
		}
	}
	return out
}

// addrKey is where an address is: sameHostKey for loopback and this server's own addresses,
// else the IP's canonical text (an IPv4-mapped IPv6 address as IPv4). "" for no address.
func addrKey(addr string, local []net.IP) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() {
		return sameHostKey
	}
	for _, l := range local {
		if l.Equal(ip) {
			return sameHostKey
		}
	}
	return ip.String()
}

// thisDeviceMatch picks the one agent whose key equals the peer's.
func thisDeviceMatch(peer string, agents []*nodeRow, local []net.IP) (*nodeRow, thisDeviceReason) {
	if len(agents) == 0 {
		return nil, reasonNoAgents
	}
	want := addrKey(peer, local)
	var hit []*nodeRow
	for _, n := range agents {
		if want != "" && n.lastAddr.Valid && addrKey(n.lastAddr.String, local) == want {
			hit = append(hit, n)
		}
	}
	switch len(hit) {
	case 1:
		return hit[0], ""
	case 0:
		return nil, reasonNoMatch
	}
	return nil, reasonAmbiguous
}

func proxied(r *http.Request) bool {
	for _, h := range forwardHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// handleThisDevice is GET /nodes/this-device[?household_id=] (user JWT). Always 200:
// {"node_id": "...", "household_id", "room", "online"} or {"node_id": null, "reason"}.
func (m *Module) handleThisDevice(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	want := r.URL.Query().Get("household_id")
	if want != "" {
		if err := m.requireRole(ctx, u.ID, want, authn.RoleMember); err != nil {
			m.writeErr(w, err)
			return
		}
	}
	cantTell := func(why thisDeviceReason) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"node_id": nil, "reason": why})
	}
	peer := peerAddr(r)
	local := localAddrs()
	if m.localIPs != nil {
		local = m.localIPs()
	}
	if addrKey(peer, local) == sameHostKey && proxied(r) {
		cantTell(reasonProxied)
		return
	}
	// The caller's own households only, superuser or not: "this computer" is the member's.
	hs, err := m.memberships(ctx, u.ID)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	mine := map[string]bool{}
	for _, h := range hs {
		if want == "" || h.HouseholdID == want {
			mine[h.HouseholdID] = true
		}
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+nodeCols+` FROM cc_nodes
		WHERE is_active = 1 AND household_id IS NOT NULL AND last_addr IS NOT NULL ORDER BY rowid`)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var agents []*nodeRow
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			m.internalError(w, err)
			return
		}
		if n.desktop() && mine[n.householdID.String] {
			agents = append(agents, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	n, why := thisDeviceMatch(peer, agents, local)
	if n == nil {
		cantTell(why)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"node_id": n.nodeID, "household_id": n.householdID.String, "room": n.room, "online": n.online(m.now()),
	})
}

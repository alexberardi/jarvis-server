// Package netaddr picks the host's LAN interfaces: the ones phones and nodes can actually
// reach. mDNS advertises only on them and the setup link and doctor use their addresses, so a
// client never resolves jarvisd to a Docker, VM or VPN bridge it can't route to.
package netaddr

import (
	"net"
	"slices"
	"strings"
)

// Iface is an interface with its addresses, so the filter can be fed made-up lists in tests.
type Iface struct {
	net.Interface
	Addrs []net.Addr
}

// List returns every interface with its addresses (nil on error).
func List() []Iface {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]Iface, 0, len(ifs))
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		out = append(out, Iface{Interface: ifc, Addrs: addrs})
	}
	return out
}

// virtualPrefixes are name prefixes (lower-cased) of container, VM and VPN interfaces:
// Docker/Podman/CNI/Kubernetes bridges, libvirt/LXC/VirtualBox/VMware/Hyper-V switches,
// macOS bridge100 (Virtualization.framework) and utun, and VPN tunnels.
var virtualPrefixes = []string{
	"docker", "br-", "veth", "virbr", "vethernet", "bridge", "utun", "tun", "tap",
	"tailscale", "wg", "zt", "vmnet", "vboxnet", "cni", "flannel", "cali", "lxc", "lxdbr",
	"podman",
}

// Virtual reports whether name looks like a container, VM or VPN interface rather than the
// LAN. Case-insensitive ("vEthernet (Default Switch)" on Windows).
func Virtual(name string) bool {
	n := strings.ToLower(name)
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// LAN filters ifs to LAN interfaces. With an allow-list, an interface qualifies when it is
// up and named in allow (case-insensitive), whatever else is true of it. Otherwise it must be
// up, multicast-capable, not loopback or point-to-point, not Virtual, and hold a private
// (RFC 1918) IPv4 address.
func LAN(ifs []Iface, allow []string) []Iface {
	var out []Iface
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		if len(allow) > 0 {
			if slices.ContainsFunc(allow, func(a string) bool { return strings.EqualFold(a, ifc.Name) }) {
				out = append(out, ifc)
			}
			continue
		}
		if ifc.Flags&net.FlagMulticast == 0 || ifc.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 || Virtual(ifc.Name) {
			continue
		}
		if PrivateIPv4(ifc) != nil {
			out = append(out, ifc)
		}
	}
	return out
}

// PrivateIPv4 is the interface's first private IPv4 address and its network, or nil.
func PrivateIPv4(ifc Iface) *net.IPNet {
	for _, a := range ifc.Addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
			return n
		}
	}
	return nil
}

// LANAddr is the first private IPv4 address on a LAN interface (see LAN), for links shown to
// the operator, or "" when there is none.
func LANAddr(allow []string) string {
	for _, ifc := range LAN(List(), allow) {
		if n := PrivateIPv4(ifc); n != nil {
			return n.IP.String()
		}
	}
	return ""
}

// LANSubnets lists the private IPv4 subnets of the LAN interfaces, deduplicated.
func LANSubnets(allow []string) []*net.IPNet {
	return Subnets(LAN(List(), allow))
}

// Subnets lists the private IPv4 subnets of ifs, deduplicated.
func Subnets(ifs []Iface) []*net.IPNet {
	var out []*net.IPNet
	for _, ifc := range ifs {
		for _, a := range ifc.Addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || !n.IP.IsPrivate() {
				continue
			}
			sub := &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
			if !slices.ContainsFunc(out, func(x *net.IPNet) bool { return x.String() == sub.String() }) {
				out = append(out, sub)
			}
		}
	}
	return out
}

package netaddr

import (
	"net"
	"slices"
	"testing"
)

const (
	up    = net.FlagUp | net.FlagMulticast | net.FlagBroadcast
	loop  = net.FlagUp | net.FlagLoopback | net.FlagMulticast
	p2p   = net.FlagUp | net.FlagPointToPoint | net.FlagMulticast
	down  = net.FlagMulticast | net.FlagBroadcast
	nomul = net.FlagUp | net.FlagBroadcast
)

func iface(name string, flags net.Flags, cidrs ...string) Iface {
	ifc := Iface{Interface: net.Interface{Name: name, Flags: flags}}
	for _, c := range cidrs {
		ip, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		n.IP = ip
		ifc.Addrs = append(ifc.Addrs, n)
	}
	return ifc
}

func names(ifs []Iface) []string {
	var out []string
	for _, i := range ifs {
		out = append(out, i.Name)
	}
	return out
}

func TestLAN(t *testing.T) {
	// The interfaces seen on the dev boxes in the I0 spike, plus the other virtual kinds.
	linux := []Iface{
		iface("lo", loop, "127.0.0.1/8", "::1/128"),
		iface("enp5s0", up, "10.0.0.122/24", "fe80::1/64"),
		iface("docker0", up, "172.17.0.1/16"),
		iface("br-3f2a9c", up, "172.18.0.1/16"),
		iface("veth12ab", up, "fe80::2/64"),
		iface("virbr0", up, "192.168.122.1/24"),
		iface("tailscale0", p2p, "100.64.0.5/32"),
		iface("wg0", p2p, "10.8.0.2/24"),
		iface("zt5u4y", up, "10.147.17.3/24"),
		iface("cni0", up, "10.244.0.1/24"),
		iface("flannel.1", up, "10.244.0.0/32"),
		iface("cali123", up, "192.168.5.1/32"),
		iface("lxcbr0", up, "10.0.3.1/24"),
		iface("lxdbr0", up, "10.10.10.1/24"),
		iface("podman0", up, "10.88.0.1/16"),
		iface("vboxnet0", up, "192.168.56.1/24"),
		iface("vmnet8", up, "192.168.200.1/24"),
	}
	mac := []Iface{
		iface("lo0", loop, "127.0.0.1/8"),
		iface("en0", up, "10.0.0.103/24", "fe80::3/64"),
		iface("bridge100", up, "192.168.64.1/24"),
		iface("utun3", p2p, "fe80::4/64"),
		iface("awdl0", up, "fe80::5/64"),
	}
	windows := []Iface{
		iface("Ethernet", up, "192.168.1.20/24"),
		iface("vEthernet (Default Switch)", up, "172.27.48.1/20"),
		iface("Loopback Pseudo-Interface 1", loop, "127.0.0.1/8"),
	}
	cases := []struct {
		name  string
		ifs   []Iface
		allow []string
		want  []string
	}{
		{"linux with docker and vpns", linux, nil, []string{"enp5s0"}},
		{"mac with bridge100", mac, nil, []string{"en0"}},
		{"windows with hyper-v", windows, nil, []string{"Ethernet"}},
		{"down", []Iface{iface("eth0", down, "10.0.0.2/24")}, nil, nil},
		{"no multicast", []Iface{iface("eth0", nomul, "10.0.0.2/24")}, nil, nil},
		{"point to point", []Iface{iface("ppp0", p2p, "10.0.0.2/24")}, nil, nil},
		{"public ipv4 only", []Iface{iface("eth0", up, "203.0.113.4/24")}, nil, nil},
		{"link-local only", []Iface{iface("eth0", up, "169.254.3.4/16", "fe80::1/64")}, nil, nil},
		{"cgnat", []Iface{iface("eth0", up, "100.64.1.1/10")}, nil, nil},
		{"two lans", []Iface{iface("eth0", up, "10.0.0.2/24"), iface("wlan0", up, "192.168.1.5/24")}, nil, []string{"eth0", "wlan0"}},
		{"real lan bridge", []Iface{iface("br0", up, "192.168.1.5/24")}, nil, []string{"br0"}},
		{"allow-list picks a virtual one", linux, []string{"docker0"}, []string{"docker0"}},
		{"allow-list is case-insensitive", windows, []string{"ethernet"}, []string{"Ethernet"}},
		{"allow-list ignores public check", []Iface{iface("eth0", up, "203.0.113.4/24")}, []string{"eth0"}, []string{"eth0"}},
		{"allow-list still needs up", []Iface{iface("eth0", down, "10.0.0.2/24")}, []string{"eth0"}, nil},
		{"allow-list no match", linux, []string{"eth9"}, nil},
		{"nothing", nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := names(LAN(c.ifs, c.allow)); !slices.Equal(got, c.want) {
				t.Fatalf("LAN = %v, want %v", got, c.want)
			}
		})
	}
}

func TestVirtual(t *testing.T) {
	for name, want := range map[string]bool{
		"docker0": true, "br-abc": true, "veth1": true, "virbr0": true, "vEthernet (WSL)": true,
		"bridge100": true, "utun0": true, "tun0": true, "tailscale0": true, "wg0": true,
		"zt0": true, "vmnet1": true, "vboxnet0": true, "cni0": true, "flannel.1": true,
		"cali1": true, "lxcbr0": true, "lxdbr0": true, "podman1": true,
		"eth0": false, "en0": false, "enp5s0": false, "wlan0": false, "wlp3s0": false,
		"br0": false, "Ethernet": false, "Wi-Fi": false,
	} {
		if got := Virtual(name); got != want {
			t.Errorf("Virtual(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestPrivateIPv4AndSubnets(t *testing.T) {
	ifs := []Iface{
		iface("eth0", up, "fe80::1/64", "203.0.113.4/24", "10.0.0.122/24"),
		iface("eth1", up, "10.0.0.123/24", "192.168.1.5/24"),
	}
	if n := PrivateIPv4(ifs[0]); n == nil || n.IP.String() != "10.0.0.122" {
		t.Fatalf("PrivateIPv4 = %v", n)
	}
	if n := PrivateIPv4(iface("x", up, "fe80::1/64")); n != nil {
		t.Fatalf("PrivateIPv4 = %v, want nil", n)
	}
	var got []string
	for _, n := range Subnets(ifs) {
		got = append(got, n.String())
	}
	if want := []string{"10.0.0.0/24", "192.168.1.0/24"}; !slices.Equal(got, want) {
		t.Fatalf("Subnets = %v, want %v", got, want)
	}
}

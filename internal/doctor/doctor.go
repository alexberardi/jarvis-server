// Package doctor checks a jarvisd install for the problems a fresh install hits: the server
// not listening, and a host firewall that lets jarvisd answer on this machine but drops what
// nodes and phones send it (Docker used to open those ports itself; a plain binary doesn't).
package doctor

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

// Status is a check's outcome.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Check is one finding. Fix, when set, is what to run or do about it.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Port is something jarvisd listens on that the LAN must reach.
type Port struct {
	Name  string
	Port  int
	Proto string // tcp or udp
}

// Options is what a run checks.
type Options struct {
	Ports []Port
	// LANs are the subnets nodes and phones connect from (default: this host's private IPv4
	// subnets).
	LANs []*net.IPNet
	// GOOS picks the firewall checks (default runtime.GOOS).
	GOOS string
	// Firewall inspects the host firewall (default: the real one for GOOS).
	Firewall Firewall
	// Dial checks a local listener (default net.Dialer, 1 s).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Firewall reports whether the host firewall admits a port from a subnet.
type Firewall interface {
	// Name is the firewall's name ("ufw", "firewalld", ...), or "" when none is active.
	Name(ctx context.Context) string
	// Allows says whether inbound p from lan is admitted. known=false: the rules can't be read
	// (permissions) and the answer is a guess.
	Allows(ctx context.Context, p Port, lan *net.IPNet) (allowed, known bool)
	// FixFor is the command (or steps) that admits ports from lan.
	FixFor(ports []Port, lan *net.IPNet) string
}

// Run performs every check.
func Run(ctx context.Context, o Options) []Check {
	if o.Dial == nil {
		d := &net.Dialer{Timeout: time.Second}
		o.Dial = d.DialContext
	}
	if o.LANs == nil {
		o.LANs = LocalLANs()
	}
	var out []Check
	out = append(out, listening(ctx, o)...)
	out = append(out, firewall(ctx, o)...)
	return out
}

func listening(ctx context.Context, o Options) []Check {
	var down []string
	for _, p := range o.Ports {
		if p.Proto != "tcp" {
			continue
		}
		c, err := o.Dial(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", p.Port))
		if err != nil {
			down = append(down, fmt.Sprintf("%s (%d)", p.Name, p.Port))
			continue
		}
		c.Close()
	}
	if len(down) > 0 {
		return []Check{{Name: "listening", Status: Fail,
			Detail: "nothing answers on " + strings.Join(down, ", "),
			Fix:    "start jarvisd (jarvisd serve), or check its log for a port already in use"}}
	}
	return []Check{{Name: "listening", Status: OK, Detail: fmt.Sprintf("jarvisd answers on all %d TCP ports", countTCP(o.Ports))}}
}

func countTCP(ps []Port) int {
	n := 0
	for _, p := range ps {
		if p.Proto == "tcp" {
			n++
		}
	}
	return n
}

func firewall(ctx context.Context, o Options) []Check {
	fw := o.Firewall
	if fw == nil {
		fw = HostFirewall(o.GOOS)
	}
	name := ""
	if fw != nil {
		name = fw.Name(ctx)
	}
	if name == "" {
		return []Check{{Name: "firewall", Status: OK, Detail: "no active host firewall found"}}
	}
	if len(o.LANs) == 0 {
		return []Check{{Name: "firewall", Status: Warn, Detail: name + " is active and this host has no private IPv4 address; nodes may not reach jarvisd"}}
	}
	var out []Check
	for _, lan := range o.LANs {
		var blocked []Port
		guessed := false
		for _, p := range o.Ports {
			ok, known := fw.Allows(ctx, p, lan)
			if !known {
				guessed = true
			}
			if !ok {
				blocked = append(blocked, p)
			}
		}
		c := Check{Name: "firewall " + lan.String(), Status: OK, Detail: name + " admits every jarvisd port from " + lan.String()}
		if len(blocked) > 0 {
			c.Status = Fail
			c.Detail = fmt.Sprintf("%s drops %s from %s: nodes and phones can't reach jarvisd, though it works from this machine",
				name, portList(blocked), lan)
			c.Fix = fw.FixFor(blocked, lan)
			if guessed {
				c.Status = Warn
				c.Detail = fmt.Sprintf("%s is active and its rules can't be read without root (run `sudo jarvisd doctor` to be sure); "+
					"unless they admit %s from %s, nodes and phones can't reach jarvisd", name, portList(blocked), lan)
			}
		}
		out = append(out, c)
	}
	return out
}

func portList(ps []Port) string {
	var s []string
	for _, p := range ps {
		s = append(s, fmt.Sprintf("%d/%s", p.Port, p.Proto))
	}
	return strings.Join(s, ", ")
}

// LocalLANs lists this host's private IPv4 subnets (RFC 1918), skipping container and VPN
// bridges' usual ranges only by being private: callers that need fewer pass LANs.
func LocalLANs() []*net.IPNet {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || virtualInterface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
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

// LANAddr is this host's address on its first private IPv4 LAN (by the same rules as
// LocalLANs), for links shown to the operator, or "" when there is none.
func LANAddr() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || virtualInterface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
				return n.IP.String()
			}
		}
	}
	return ""
}

// virtualInterface is a container bridge or VPN, not the LAN nodes are on.
func virtualInterface(name string) bool {
	for _, p := range []string{"docker", "br-", "veth", "virbr", "tailscale", "tun", "wg", "zt", "vboxnet", "vmnet", "lxc", "cni", "flannel"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

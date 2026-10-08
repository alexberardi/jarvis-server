// Package doctor checks a jarvisd install for the problems a fresh install hits: the server
// not listening (or another program, such as the legacy Docker stack, holding its ports), a
// host firewall that lets jarvisd answer on this machine but drops what nodes and phones send
// it (Docker used to open those ports itself; a plain binary doesn't), a data directory others
// can read, and a Mac that sleeps.
package doctor

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/netaddr"
)

// Status is a check's outcome.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Check is one finding. Fix, when set, is what to run or do about it. FixCmds are the same
// commands as argv lists (without sudo), for `jarvisd doctor --fix` and the installers;
// only the firewall checks set them.
type Check struct {
	Name    string     `json:"name"`
	Status  Status     `json:"status"`
	Detail  string     `json:"detail"`
	Fix     string     `json:"fix,omitempty"`
	FixCmds [][]string `json:"fix_cmds,omitempty"`
}

// Port is something jarvisd listens on that the LAN must reach.
type Port struct {
	Name  string
	Port  int
	Proto string // tcp or udp
}

// http reports whether jarvisd serves HTTP on the port (every TCP listener but the MQTT
// broker's).
func (p Port) http() bool {
	return p.Proto == "tcp" && p.Name != "mqtt" && p.Name != "mqtt-ws"
}

// Options is what a run checks.
type Options struct {
	Ports []Port
	// LANs are the subnets nodes and phones connect from (default: this host's private IPv4
	// subnets).
	LANs []*net.IPNet
	// Interfaces names the LAN interfaces LANs defaults from (JARVIS_MDNS_INTERFACES); empty
	// picks them automatically.
	Interfaces []string
	// GOOS picks the OS-specific checks (default runtime.GOOS).
	GOOS string
	// Firewall inspects the host firewall (default: the real one for GOOS).
	Firewall Firewall
	// Dial checks a local listener (default net.Dialer, 1 s).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// ServerHeader GETs a URL and returns its Server header (default: an HTTP client, 2 s).
	ServerHeader func(ctx context.Context, url string) (string, error)
	// Run runs the external commands the OS checks use (docker, nvidia-smi, pmset, netsh).
	Run Runner
	// Home is jarvisd's data directory; "" skips the permission checks (the admin's
	// in-process doctor runs as the owner and secures it at start anyway).
	Home string
	// LegacyDirs are the legacy stack's directories to look for (default: ~/.jarvis of this
	// user and of $SUDO_USER).
	LegacyDirs []string
	// ReadFile reads /proc for the GPU memory check (default os.ReadFile).
	ReadFile func(name string) ([]byte, error)
	// LegacyUID is the account whose LaunchAgents (macOS) the legacy check looks at (default:
	// this user, or $SUDO_UID under sudo; "" with neither skips them).
	LegacyUID string
}

// Firewall reports whether the host firewall admits a port from a subnet, and how to make
// it.
type Firewall interface {
	// Name is the firewall's name ("ufw", "firewalld", ...), or "" when none is active.
	Name(ctx context.Context) string
	// Allows says whether inbound p from lan is admitted. known=false: the rules can't be read
	// (permissions) and the answer is a guess.
	Allows(ctx context.Context, p Port, lan *net.IPNet) (allowed, known bool)
	// FixCmds are the commands (argv, run with root/Administrator rights) that admit ports
	// from lan. The rules they add carry the jarvisd tag so RemoveCmds finds them, and
	// re-running them is harmless.
	FixCmds(ctx context.Context, ports []Port, lan *net.IPNet) [][]string
	// FixNote is advice shown after the commands, "" for none.
	FixNote() string
	// RemoveCmds deletes every rule FixCmds added (for `jarvisd service uninstall`).
	RemoveCmds(ctx context.Context) [][]string
}

// Run performs every check.
func Run(ctx context.Context, o Options) []Check {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Dial == nil {
		d := &net.Dialer{Timeout: time.Second}
		o.Dial = d.DialContext
	}
	if o.ServerHeader == nil {
		o.ServerHeader = serverHeader
	}
	if o.Run == nil {
		o.Run = execRunner
	}
	if o.LANs == nil {
		o.LANs = LocalLANs(o.Interfaces)
	}
	if o.LegacyDirs == nil {
		o.LegacyDirs = legacyDirs()
	}
	if o.LegacyUID == "" && o.GOOS == "darwin" {
		o.LegacyUID = legacyUID()
	}
	out := listening(ctx, o)
	portsHeld := out[0].Name == "ports"
	out = append(out, firewall(ctx, o)...)
	out = append(out, osChecks(ctx, o)...)
	out = append(out, permissions(o.Home)...)
	out = append(out, legacy(ctx, o, portsHeld)...)
	return out
}

func serverHeader(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	return resp.Header.Get("Server"), nil
}

// listening dials every TCP port. Something answering on an HTTP listener without jarvisd's
// Server header is another program (typically the legacy stack), which keeps jarvisd from
// starting at all: it binds every listener before serving.
func listening(ctx context.Context, o Options) []Check {
	var down, foreign []Port
	jarvisd := false // some HTTP listener is jarvisd's
	var other []Port // non-HTTP ports that answer
	for _, p := range o.Ports {
		if p.Proto != "tcp" {
			continue
		}
		c, err := o.Dial(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", p.Port))
		if err != nil {
			down = append(down, p)
			continue
		}
		c.Close()
		if !p.http() {
			other = append(other, p)
			continue
		}
		srv, err := o.ServerHeader(ctx, fmt.Sprintf("http://127.0.0.1:%d/health", p.Port))
		if err == nil && strings.EqualFold(srv, httpx.ServerName) {
			jarvisd = true
		} else {
			foreign = append(foreign, p)
		}
	}
	// The broker's ports can't be asked; they are jarvisd's when its HTTP listeners are.
	if !jarvisd {
		foreign = append(foreign, other...)
	}
	var out []Check
	if len(foreign) > 0 {
		out = append(out, Check{Name: "ports", Status: Fail,
			Detail: "another program holds " + names(foreign) + ", so jarvisd can't start (it needs every port); " +
				"if it is the legacy Jarvis Docker stack, stop it first",
			Fix: whoHolds(o.GOOS, foreign[0].Port)})
	}
	switch {
	case len(down) > 0 && len(foreign) == 0:
		out = append(out, Check{Name: "listening", Status: Fail,
			Detail: "nothing answers on " + names(down),
			Fix:    "start jarvisd (jarvisd service start, or jarvisd serve), or check its log for a port already in use"})
	case len(down) > 0:
		out = append(out, Check{Name: "listening", Status: Fail, Detail: "nothing answers on " + names(down)})
	case len(foreign) == 0:
		out = append(out, Check{Name: "listening", Status: OK, Detail: fmt.Sprintf("jarvisd answers on all %d TCP ports", countTCP(o.Ports))})
	}
	return out
}

func names(ps []Port) string {
	var s []string
	for _, p := range ps {
		s = append(s, fmt.Sprintf("%s (%d)", p.Name, p.Port))
	}
	return strings.Join(s, ", ")
}

// whoHolds is the command that names the program listening on port.
func whoHolds(goos string, port int) string {
	switch goos {
	case "windows":
		return fmt.Sprintf("Get-Process -Id (Get-NetTCPConnection -LocalPort %d -State Listen).OwningProcess", port)
	case "darwin":
		return fmt.Sprintf("sudo lsof -nP -iTCP:%d -sTCP:LISTEN", port)
	}
	return fmt.Sprintf("sudo ss -ltnp 'sport = :%d'", port)
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
			// Only ever a private LAN: never open jarvisd to the internet.
			if PrivateLAN(lan) {
				c.FixCmds = fw.FixCmds(ctx, blocked, lan)
			}
			c.Fix = RenderFix(o.GOOS, c.FixCmds, fw.FixNote())
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

// PrivateLAN reports whether n is a private IPv4 subnet no wider than a /8: the only kind of
// source the firewall fix admits.
func PrivateLAN(n *net.IPNet) bool {
	if n == nil || n.IP.To4() == nil || !n.IP.IsPrivate() {
		return false
	}
	ones, bits := n.Mask.Size()
	return bits == 32 && ones >= 8
}

func portList(ps []Port) string {
	var s []string
	for _, p := range ps {
		s = append(s, fmt.Sprintf("%d/%s", p.Port, p.Proto))
	}
	return strings.Join(s, ", ")
}

// LocalLANs lists the private IPv4 subnets of this host's LAN interfaces (netaddr.LAN, so
// container, VM and VPN bridges are left out; allow overrides the choice as
// JARVIS_MDNS_INTERFACES does).
func LocalLANs(allow []string) []*net.IPNet {
	return netaddr.LANSubnets(allow)
}

// LANAddr is this host's address on its first LAN interface (same rules as mDNS), for links
// shown to the operator, or "" when there is none.
func LANAddr(allow []string) string {
	return netaddr.LANAddr(allow)
}

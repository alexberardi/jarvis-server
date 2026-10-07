package doctor

import (
	"net"
	"strconv"
)

// Exposure is what jarvisd exposes to the LAN: the listeners it serves, the MQTT broker's
// TCP and WebSocket listen addresses, and whether it advertises over mDNS. `jarvisd doctor`
// and the admin's GET /api/doctor check the same set.
type Exposure struct {
	Listeners  []string // served listener names, e.g. "auth", "cc"
	MQTTAddr   string   // e.g. ":1883"
	MQTTWSAddr string
	MDNS       bool
}

// Ports lists what the LAN must reach, with each listener's port from ports (a listener
// without a port, or port 0, is skipped; a port is listed once).
func (e Exposure) Ports(ports map[string]int) []Port {
	var out []Port
	seen := map[int]bool{}
	add := func(name string, port int, proto string) {
		if port > 0 && !seen[port] {
			seen[port] = true
			out = append(out, Port{Name: name, Port: port, Proto: proto})
		}
	}
	for _, l := range e.Listeners {
		add(l, ports[l], "tcp")
	}
	add("mqtt", PortOf(e.MQTTAddr), "tcp")
	add("mqtt-ws", PortOf(e.MQTTWSAddr), "tcp")
	if e.MDNS {
		add("mdns", 5353, "udp")
	}
	return out
}

// PortOf is the port of a listen address like ":1884", or 0.
func PortOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// Worst is the most severe status among checks (OK when there are none).
func Worst(checks []Check) Status {
	w := OK
	for _, c := range checks {
		switch {
		case c.Status == Fail:
			return Fail
		case c.Status == Warn:
			w = Warn
		}
	}
	return w
}

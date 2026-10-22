package sip

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Remote SDP is checked and rewritten here before diago sees it. diago reads SDP as a flat
// map (every c= and a= line of every level together), takes the first c= (so a media-level
// c= after a session-level one is ignored), refuses more than one m= line, indexes a c= line's
// fields without checking them, and writes one codec per a=rtpmap line into a slice sized by
// the m= line's formats (a repeated rtpmap overruns it). canonicalSDP keeps only what a
// G.711 call needs, in a shape diago reads correctly: the audio m= section, one session-level
// c= carrying the address that applies to the audio, and well-formed, de-duplicated rtpmaps.

// remoteSDP is a checked remote description.
type remoteSDP struct {
	body    []byte // canonical: what diago gets
	ip      net.IP
	port    int
	formats []string
	dtmfPT  int // telephone-event/8000's payload type; -1 when not offered
}

var errSDP = errors.New("sip: unusable SDP")

func sdpErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errSDP, fmt.Sprintf(format, a...))
}

// canonicalSDP checks b and rewrites it (see above). It never panics.
func canonicalSDP(b []byte) (*remoteSDP, error) {
	if len(b) == 0 {
		return nil, sdpErr("empty")
	}
	if len(b) > 64<<10 {
		return nil, sdpErr("%d bytes", len(b))
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var session []string // session-level lines, in order, without c=
	var sessionC, mediaC, mline string
	var mattrs []string
	inAudio, seenM := false, false
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		if len(line) < 2 || line[1] != '=' {
			return nil, sdpErr("not a type=value line: %q", clip(line))
		}
		key, val := line[0], line[2:]
		if key == 'm' {
			seenM = true
			inAudio = strings.HasPrefix(val, "audio ") && mline == ""
			if inAudio {
				mline = val
			}
			continue
		}
		switch {
		case !seenM && key == 'c':
			sessionC = val
		case !seenM:
			session = append(session, line)
		case !inAudio:
			// another media section (video, a second audio): not ours
		case key == 'c':
			mediaC = val
		case key == 'a':
			mattrs = append(mattrs, val)
		}
	}
	if mline == "" {
		return nil, sdpErr("no audio m= line")
	}
	r := &remoteSDP{dtmfPT: -1}

	mf := strings.Fields(mline)
	if len(mf) < 4 {
		return nil, sdpErr("m= line %q", clip(mline))
	}
	port, err := strconv.Atoi(strings.SplitN(mf[1], "/", 2)[0])
	if err != nil || port < 0 || port > 65535 {
		return nil, sdpErr("m= port %q", clip(mf[1]))
	}
	r.port, r.formats = port, mf[3:]

	conn := mediaC
	if conn == "" {
		conn = sessionC
	}
	ip, addrType, err := parseConnection(conn)
	if err != nil {
		return nil, err
	}
	r.ip = ip

	var origin bool
	for _, l := range session {
		if strings.HasPrefix(l, "o=") {
			of := strings.Fields(l[2:])
			if len(of) < 6 {
				return nil, sdpErr("o= line %q", clip(l))
			}
			if _, err := strconv.ParseUint(of[1], 10, 64); err != nil {
				return nil, sdpErr("o= session id %q", clip(of[1]))
			}
			if _, err := strconv.ParseUint(of[2], 10, 64); err != nil {
				return nil, sdpErr("o= session version %q", clip(of[2]))
			}
			origin = true
		}
	}
	if !origin {
		return nil, sdpErr("no o= line")
	}

	// Session-level a= lines (direction) stay; the audio section's attributes follow, with
	// each rtpmap checked and kept once per payload type.
	var out bytes.Buffer
	for _, l := range session {
		if strings.HasPrefix(l, "a=rtpmap:") {
			continue // an rtpmap belongs to a media section
		}
		out.WriteString(l + "\r\n")
	}
	out.WriteString("c=IN " + addrType + " " + ip.String() + "\r\n")
	out.WriteString("m=" + strings.Join(mf, " ") + "\r\n")
	seen := map[string]bool{}
	for _, a := range mattrs {
		if !strings.HasPrefix(a, "rtpmap:") {
			out.WriteString("a=" + a + "\r\n")
			continue
		}
		pt, name, rate, ok := parseRtpmap(a)
		if !ok || seen[pt] {
			continue
		}
		seen[pt] = true
		out.WriteString("a=" + a + "\r\n")
		if strings.EqualFold(name, "telephone-event") && rate == 8000 && r.dtmfPT < 0 && contains(r.formats, pt) {
			n, _ := strconv.Atoi(pt)
			r.dtmfPT = n
		}
	}
	r.body = out.Bytes()
	return r, nil
}

// parseConnection reads "IN IP4 192.0.2.1" (with an optional /ttl).
func parseConnection(c string) (net.IP, string, error) {
	f := strings.Fields(c)
	if len(f) < 3 || f[0] != "IN" || (f[1] != "IP4" && f[1] != "IP6") {
		return nil, "", sdpErr("c= line %q", clip(c))
	}
	ip := net.ParseIP(strings.SplitN(f[2], "/", 2)[0])
	if ip == nil {
		return nil, "", sdpErr("c= address %q", clip(f[2]))
	}
	if f[1] == "IP4" {
		if ip = ip.To4(); ip == nil {
			return nil, "", sdpErr("c= address %q is not IPv4", clip(f[2]))
		}
	}
	return ip, f[1], nil
}

// parseRtpmap reads "rtpmap:<pt> <name>/<rate>[/<channels>]".
func parseRtpmap(a string) (pt, name string, rate int, ok bool) {
	pt, rest, found := strings.Cut(strings.TrimPrefix(a, "rtpmap:"), " ")
	if !found {
		return "", "", 0, false
	}
	if n, err := strconv.ParseUint(pt, 10, 8); err != nil || n > 127 {
		return "", "", 0, false
	}
	enc := strings.Split(strings.Fields(rest + " x")[0], "/")
	if len(enc) < 2 || enc[0] == "" {
		return "", "", 0, false
	}
	r, err := strconv.ParseUint(enc[1], 10, 32)
	if err != nil || r == 0 {
		return "", "", 0, false
	}
	return pt, enc[0], int(r), true
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func clip(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

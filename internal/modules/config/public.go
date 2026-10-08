package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Public base URLs (cutover Q1). A host reachable from outside the LAN through a reverse tunnel
// (prod: cloudflared, one public hostname per service) gives each registry row a public base
// URL, e.g. https://command-center.example.io or wss://mqtt.example.io. It is stored in the
// row's external_scheme/external_host/external_port, which syncSelf never writes, so it
// survives every restart.
//
// Discovery "answers in kind": /services and /services/{name} return a row's public URL when
// the request itself came in through a public hostname, and its LAN URL otherwise. Legacy
// ?style=external (the mobile app always asks for it) returns the public URL too, as the
// legacy registry did when its rows held the public names. ?style=remote (nodes on the LAN
// pointed at the config IP) and dockerized keep their LAN answers unless the request itself
// came through a public hostname.
//
// "Came in through a public hostname" is the request's Host header matching one of the
// configured public hosts: Cloudflare forwards the visitor's Host unchanged to the origin.
// From a loopback peer only (cloudflared runs on the same machine and dials localhost),
// X-Forwarded-Host or CF-Connecting-IP also count, for a tunnel whose ingress rewrites Host.
//
// Why trusting these headers is safe: the decision only picks which of two operator-entered
// URLs to hand back from an unauthenticated, read-only endpoint, and any caller can already
// get the public one with ?style=external. A forged Host gains nothing and can't reach any
// authentication or authorization decision. The loopback rule just keeps a stray header from a
// LAN client from sending it the long way round.

// defaultPorts are the well-known ports a URL without one implies.
var defaultPorts = map[string]int{"http": 80, "https": 443, "ws": 80, "wss": 443, "mqtt": 1883, "mqtts": 8883}

// publicSchemes is what a public URL may use for a row of each scheme: HTTP services stay
// HTTP(S); the MQTT broker may be published as MQTT or as MQTT over WebSocket (prod: wss).
var publicSchemes = map[string][]string{
	"http":  {"http", "https"},
	"https": {"http", "https"},
	"ws":    {"ws", "wss"},
	"wss":   {"ws", "wss"},
	"mqtt":  {"mqtt", "mqtts", "ws", "wss"},
	"mqtts": {"mqtt", "mqtts", "ws", "wss"},
}

// hasPublic reports whether external_* hold a public base URL.
func (s service) hasPublic() bool {
	return s.ExternalScheme.Valid && s.ExternalScheme.String != "" && s.ExternalHost.Valid && s.ExternalHost.String != ""
}

// publicURL is the row's public base URL in the registry's scheme://host:port form (legacy
// rendered public rows the same way, e.g. https://command-center.example.io:443).
func (s service) publicURL() (string, bool) {
	if !s.hasPublic() {
		return "", false
	}
	port := defaultPorts[s.ExternalScheme.String]
	if s.ExternalPort.Valid && s.ExternalPort.Int64 != 0 {
		port = int(s.ExternalPort.Int64)
	}
	return fmt.Sprintf("%s://%s:%d", s.ExternalScheme.String, s.ExternalHost.String, port), true
}

// publicDisplay is the public URL as the operator entered it (no default port), or "".
func (s service) publicDisplay() string {
	if !s.hasPublic() {
		return ""
	}
	u := s.ExternalScheme.String + "://" + s.ExternalHost.String
	if s.ExternalPort.Valid && s.ExternalPort.Int64 != 0 {
		u += ":" + strconv.FormatInt(s.ExternalPort.Int64, 10)
	}
	return u
}

// publicHosts is the set of configured public hostnames (lower case).
func publicHosts(svcs []service) map[string]bool {
	out := map[string]bool{}
	for _, s := range svcs {
		if s.hasPublic() {
			out[normHost(s.ExternalHost.String)] = true
		}
	}
	return out
}

// normHost lower-cases a Host value and drops its port, IPv6 brackets and a trailing dot.
func normHost(h string) string {
	h = strings.TrimSpace(h)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

func loopbackPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// viaPublicHost reports whether r arrived through one of the public hostnames (see the top of
// this file for why the headers may be believed here).
func viaPublicHost(r *http.Request, hosts map[string]bool) bool {
	if len(hosts) == 0 {
		return false
	}
	if hosts[normHost(r.Host)] {
		return true
	}
	if !loopbackPeer(r) {
		return false
	}
	if xf := r.Header.Get("X-Forwarded-Host"); xf != "" {
		first, _, _ := strings.Cut(xf, ",")
		if hosts[normHost(first)] {
			return true
		}
	}
	return r.Header.Get("Cf-Connecting-Ip") != ""
}

// publicParts is a validated public URL.
type publicParts struct {
	scheme, host string
	port         int // 0: the scheme's default
}

// parsePublicURL validates a public base URL for a row of rowScheme: scheme
// http/https/ws/wss/mqtt/mqtts (matching the row's protocol), a host, an optional port, and
// nothing else (no path, user, query or fragment).
func parsePublicURL(raw, rowScheme, field string) (publicParts, []httpx.FieldError) {
	bad := func(msg string) (publicParts, []httpx.FieldError) {
		return publicParts{}, []httpx.FieldError{{Type: "value_error", Loc: []any{"body", field}, Msg: msg, Input: raw}}
	}
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return bad("Input should be a URL like https://jarvis.example.com")
	}
	scheme := strings.ToLower(u.Scheme)
	if _, ok := defaultPorts[scheme]; !ok {
		return bad("The scheme must be http, https, ws, wss, mqtt or mqtts")
	}
	if allowed, ok := publicSchemes[rowScheme]; ok {
		match := false
		for _, a := range allowed {
			match = match || a == scheme
		}
		if !match {
			return bad(fmt.Sprintf("A %s service can be published as %s only", rowScheme, strings.Join(allowed, ", ")))
		}
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return bad("The public URL must be a base URL: scheme, host and optional port, no path")
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
		if !ipv6Lit.MatchString(host) {
			return bad("Invalid IPv6 host")
		}
	} else if !bareHost.MatchString(host) || len(host) > 255 {
		return bad("The host must be a hostname or IP address")
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); host == "localhost" || (err == nil && ip.IsLoopback()) {
		return bad("A public URL can't point at this machine's loopback address")
	}
	port := 0
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return bad("The port must be between 1 and 65535")
		}
		port = n
	}
	return publicParts{scheme: scheme, host: host, port: port}, nil
}

// SetPublicURL sets (or, with "", clears) a row's public base URL. Any row may have one: a
// jarvisd listener, the MQTT broker, or an external entry. Errors: *ValidationError,
// ErrServiceNotFound.
func (m *Module) SetPublicURL(ctx context.Context, name, raw string) (ServiceEntry, error) {
	s, err := m.byName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceEntry{}, ErrServiceNotFound
	}
	if err != nil {
		return ServiceEntry{}, err
	}
	if strings.TrimSpace(raw) == "" {
		_, err = m.deps.DB.Write.ExecContext(ctx, `UPDATE config_services SET external_scheme = NULL,
			external_host = NULL, external_port = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			WHERE name = ?`, name)
	} else {
		p, errs := parsePublicURL(raw, s.Scheme, "public_url")
		if len(errs) > 0 {
			return ServiceEntry{}, &ValidationError{Fields: errs}
		}
		err = m.writePublic(ctx, name, p)
	}
	if err != nil {
		return ServiceEntry{}, err
	}
	return m.entry(ctx, name)
}

func (m *Module) writePublic(ctx context.Context, name string, p publicParts) error {
	var port any
	if p.port != 0 {
		port = p.port
	}
	_, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE config_services SET external_scheme = ?,
		external_host = ?, external_port = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE name = ?`, p.scheme, p.host, port, name)
	return err
}

// entry is one row as the admin shows it.
func (m *Module) entry(ctx context.Context, name string) (ServiceEntry, error) {
	s, err := m.byName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceEntry{}, ErrServiceNotFound
	}
	if err != nil {
		return ServiceEntry{}, err
	}
	return m.toEntry(s, m.managed(ctx)), nil
}

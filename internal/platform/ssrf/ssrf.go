// Package ssrf is jarvisd's guarded outbound HTTP for fetching URLs a user (or a model) named:
// cc's quick_search/deep_research page scrapes and the phone plan fetch, and recipes' URL
// preflight. It is the legacy guard both quick_search (_is_blocked_host / _safe_get) and
// jarvis-recipes-server (html_fetcher._ip_blocked / _request_following_redirects) used — the
// two rule sets are the same — plus a dialer that re-checks the address it actually connects
// to, which also closes DNS rebinding between the resolve-then-check and the connect.
//
//   - Blocked: loopback, private, link-local (169.254/16 cloud metadata, fe80::/10), reserved
//     (incl. NAT64), multicast, unspecified and every other non-global range (CGNAT, the
//     documentation nets, ...). IPv4-mapped IPv6 is judged as its embedded IPv4.
//   - A host is blocked when it is "localhost", when ANY address it resolves to is blocked
//     (split-horizon DNS), or when it does not resolve (fail closed).
//   - Redirects are followed by hand (at most 5 hops), re-checking every hop; Authorization,
//     Cookie and Proxy-Authorization are dropped on a hop to another origin (scheme, host,
//     port: the recipes rule, which is the stricter of the two legacy rules).
package ssrf

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Defaults (legacy quick_search: 8 s per page, 5 redirect hops).
const (
	DefaultTimeout      = 8 * time.Second
	DefaultMaxRedirects = 5
	DefaultMaxBytes     = 4 << 20 // response body cap (legacy had none)
)

// ErrBlockedHost is returned for a URL (or redirect hop) pointing at a private or disallowed
// host. The message is the legacy one.
var ErrBlockedHost = errors.New("URL points to a private or disallowed host")

var (
	// ErrInvalidURL is a URL or redirect target that is not absolute http(s) with a host.
	ErrInvalidURL = errors.New("Invalid URL or redirect target")
	// ErrTooManyRedirects is a redirect chain longer than MaxRedirects.
	ErrTooManyRedirects = errors.New("Too many redirects")
)

// nonGlobal are the special-purpose ranges Python's ipaddress treats as not global (plus
// multicast and reserved, which _ip_blocked lists explicitly).
var nonGlobal = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		// IPv4
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		// IPv6
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23",
		"2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var globalUnicast6 = netip.MustParsePrefix("2000::/3")

// IPBlocked is the legacy _ip_blocked (see the package comment).
func IPBlocked(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	ip = ip.Unmap()
	if ip.Zone() != "" {
		ip = ip.WithZone("")
	}
	// Python treats everything outside global unicast 2000::/3 as reserved (::/8, 100::/8,
	// 4000::/3, ..., fe00::/9) or link/site-local, multicast, ULA.
	if ip.Is6() && !globalUnicast6.Contains(ip) {
		return true
	}
	for _, p := range nonGlobal {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Fetcher is the guarded HTTP client. Zero values take the defaults; the hooks exist for tests.
type Fetcher struct {
	Timeout      time.Duration
	MaxRedirects int
	MaxBytes     int64
	// Blocked decides whether an address may be contacted (default IPBlocked).
	Blocked func(netip.Addr) bool
	// LookupIP resolves a host name (default net.DefaultResolver).
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (f *Fetcher) blocked(ip netip.Addr) bool {
	if f != nil && f.Blocked != nil {
		return f.Blocked(ip)
	}
	return IPBlocked(ip)
}

func (f *Fetcher) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if f != nil && f.LookupIP != nil {
		return f.LookupIP(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// IsBlockedHost is the legacy _is_blocked_host: true when host is, or DNS-resolves to, a
// disallowed address. Any resolved address being unsafe blocks; an unresolvable name fails
// closed.
func (f *Fetcher) IsBlockedHost(ctx context.Context, host string) bool {
	if host == "" {
		return true
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return f.blocked(ip)
	}
	if h := strings.ToLower(host); h == "localhost" || h == "localhost." {
		return true
	}
	addrs, err := f.lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return true
	}
	for _, a := range addrs {
		if f.blocked(a) {
			return true
		}
	}
	return false
}

func (f *Fetcher) timeout() time.Duration {
	if f != nil && f.Timeout > 0 {
		return f.Timeout
	}
	return DefaultTimeout
}

// Client builds a per-call client: no automatic redirects (Do walks and re-validates each
// hop), and a dialer that refuses a disallowed connected address.
func (f *Fetcher) Client() *http.Client {
	timeout := f.timeout()
	dialer := &net.Dialer{
		Timeout: timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || f.blocked(ip) {
				return ErrBlockedHost
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil, // never route egress through an env proxy
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

var sensitiveHeaders = []string{"Authorization", "Cookie", "Proxy-Authorization"}

func isRedirect(code int) bool {
	switch code {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

// origin is (scheme, host, port) with the scheme's default port filled in.
func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// Response is a fetched response: the final hop's status, headers and body (capped at
// MaxBytes; empty for HEAD).
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	URL    string // the final URL after redirects
}

// Do sends method to rawURL, following at most MaxRedirects redirects by hand so every hop's
// host is re-validated; credentials are dropped when a hop changes origin.
func (f *Fetcher) Do(ctx context.Context, method, rawURL string, headers http.Header) (*Response, error) {
	maxRedirects := DefaultMaxRedirects
	maxBytes := int64(DefaultMaxBytes)
	if f != nil && f.MaxRedirects > 0 {
		maxRedirects = f.MaxRedirects
	}
	if f != nil && f.MaxBytes > 0 {
		maxBytes = f.MaxBytes
	}
	c := f.Client()
	defer c.CloseIdleConnections()
	hdr := headers.Clone()
	if hdr == nil {
		hdr = http.Header{}
	}
	current := rawURL
	for hop := 0; hop <= maxRedirects; hop++ {
		u, err := url.Parse(current)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil, ErrInvalidURL
		}
		if f.IsBlockedHost(ctx, u.Hostname()) {
			return nil, ErrBlockedHost
		}
		req, err := http.NewRequestWithContext(ctx, method, current, nil)
		if err != nil {
			return nil, ErrInvalidURL
		}
		req.Header = hdr.Clone()
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		if isRedirect(resp.StatusCode) && resp.Header.Get("Location") != "" {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			next, err := u.Parse(loc)
			if err != nil {
				return nil, ErrInvalidURL
			}
			if origin(next) != origin(u) {
				for _, h := range sensitiveHeaders {
					hdr.Del(h)
				}
			}
			current = next.String()
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
		resp.Body.Close()
		return &Response{Status: resp.StatusCode, Header: resp.Header, Body: body, URL: current}, err
	}
	return nil, ErrTooManyRedirects
}

// Get is Do with GET, returning the final status and body.
func (f *Fetcher) Get(ctx context.Context, rawURL string, headers http.Header) (int, []byte, error) {
	r, err := f.Do(ctx, http.MethodGet, rawURL, headers)
	if r == nil {
		return 0, nil, err
	}
	return r.Status, r.Body, err
}

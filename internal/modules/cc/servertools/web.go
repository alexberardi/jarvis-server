package servertools

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// Web egress for quick_search and deep_research (docs/cc/04 §3.11-3.12, §11 "Web").
//
// The SSRF guard is the legacy quick_search one (_is_blocked_host / _safe_get), plus the
// doc 04 §11 hardening: the dialer re-checks the IP it actually connects to, which also
// closes DNS rebinding between the resolve-then-check and the connect.

// Fetch limits (legacy quick_search: 8 s per page, 5 redirect hops).
const (
	fetchTimeout      = 8 * time.Second
	fetchMaxRedirects = 5
	fetchMaxBytes     = 4 << 20 // response body cap (legacy had none)
	fetchUserAgent    = "Mozilla/5.0 (compatible; JarvisBot/1.0)"
)

// ErrBlockedHost is returned for a URL (or redirect hop) pointing at a private or disallowed
// host. The message is the legacy one.
var ErrBlockedHost = errors.New("URL points to a private or disallowed host")

var (
	errInvalidURL       = errors.New("Invalid URL or redirect target")
	errTooManyRedirects = errors.New("Too many redirects")
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

// IPBlocked is the legacy _ip_blocked: loopback, private, link-local (169.254/16 cloud
// metadata, fe80::/10), reserved (incl. NAT64), multicast, unspecified and every other
// non-global range (CGNAT 100.64/10, documentation nets, ...). IPv4-mapped IPv6 is judged as
// its embedded IPv4.
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

// Fetcher is the SSRF-guarded HTTP GET (legacy _safe_get). Zero values take the defaults;
// the hooks exist for tests.
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

// client builds the per-call client: no automatic redirects (each hop is walked and
// re-validated by Get), and a dialer that refuses a disallowed connected address.
func (f *Fetcher) client() *http.Client {
	timeout := fetchTimeout
	if f != nil && f.Timeout > 0 {
		timeout = f.Timeout
	}
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

// Get fetches rawURL, following at most MaxRedirects redirects by hand so every hop's host
// is re-validated; credentials are dropped when a hop changes host. It returns the final
// status and the body (capped at MaxBytes).
func (f *Fetcher) Get(ctx context.Context, rawURL string, headers http.Header) (int, []byte, error) {
	maxRedirects := fetchMaxRedirects
	maxBytes := int64(fetchMaxBytes)
	if f != nil && f.MaxRedirects > 0 {
		maxRedirects = f.MaxRedirects
	}
	if f != nil && f.MaxBytes > 0 {
		maxBytes = f.MaxBytes
	}
	c := f.client()
	defer c.CloseIdleConnections()
	hdr := headers.Clone()
	if hdr == nil {
		hdr = http.Header{}
	}
	current := rawURL
	for hop := 0; hop <= maxRedirects; hop++ {
		u, err := url.Parse(current)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return 0, nil, errInvalidURL
		}
		if f.IsBlockedHost(ctx, u.Hostname()) {
			return 0, nil, ErrBlockedHost
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return 0, nil, errInvalidURL
		}
		req.Header = hdr.Clone()
		resp, err := c.Do(req)
		if err != nil {
			return 0, nil, err
		}
		if isRedirect(resp.StatusCode) && resp.Header.Get("Location") != "" {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			next, err := u.Parse(loc)
			if err != nil {
				return 0, nil, errInvalidURL
			}
			if !strings.EqualFold(next.Hostname(), u.Hostname()) {
				for _, h := range sensitiveHeaders {
					hdr.Del(h)
				}
			}
			current = next.String()
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
		resp.Body.Close()
		return resp.StatusCode, body, err
	}
	return 0, nil, errTooManyRedirects
}

var (
	scriptRE = regexp.MustCompile(`(?i)<script[^>]*>[\s\S]*?</script>`)
	styleRE  = regexp.MustCompile(`(?i)<style[^>]*>[\s\S]*?</style>`)
	tagRE    = regexp.MustCompile(`<[^>]+>`)
	titleRE  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

// ExtractText is quick_search's _extract_text: drop script/style blocks and tags, decode the
// common entities, collapse whitespace.
func ExtractText(page string) string {
	t := scriptRE.ReplaceAllString(page, "")
	t = styleRE.ReplaceAllString(t, "")
	t = tagRE.ReplaceAllString(t, " ")
	t = strings.ReplaceAll(t, "&amp;", "&")
	t = strings.ReplaceAll(t, "&lt;", "<")
	t = strings.ReplaceAll(t, "&gt;", ">")
	t = strings.ReplaceAll(t, "&quot;", `"`)
	t = strings.ReplaceAll(t, "&#39;", "'")
	t = strings.ReplaceAll(t, "&nbsp;", " ")
	return collapseSpace(t)
}

// collapseSpace is re.sub(r"\s+", " ", s).strip() with Python's Unicode whitespace.
func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if parse.IsPySpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// pageTitle is the document <title>, unescaped and collapsed ("" when absent).
func pageTitle(page string) string {
	m := titleRE.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return collapseSpace(html.UnescapeString(tagRE.ReplaceAllString(m[1], " ")))
}

// truncRunes is s[:n] on code points.
func truncRunes(s string, n int) string {
	i := 0
	for p := range s {
		if i == n {
			return s[:p]
		}
		i++
	}
	return s
}

func runeLen(s string) int { return len([]rune(s)) }

// SearchResult is one web search hit (ddgs text(): title, href, body).
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// WebSearcher runs a web search.
type WebSearcher interface {
	Search(ctx context.Context, query string, max int) ([]SearchResult, error)
}

// DuckDuckGo searches DuckDuckGo's HTML endpoint (what the ddgs package scrapes; DDG has no
// official API). It goes straight to the public endpoint, never through the SSRF fetcher.
type DuckDuckGo struct {
	// Endpoint defaults to https://html.duckduckgo.com/html/.
	Endpoint string
	Client   *http.Client
}

var (
	ddgAnchorRE  = regexp.MustCompile(`(?s)<a\s[^>]*class="[^"]*\bresult__a\b[^"]*"[^>]*>(.*?)</a>`)
	ddgSnippetRE = regexp.MustCompile(`(?s)<a\s[^>]*class="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</a>`)
	hrefRE       = regexp.MustCompile(`\shref="([^"]*)"`)
)

// Search posts the query and parses up to max organic results.
func (d *DuckDuckGo) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	ep := d.Endpoint
	if ep == "" {
		ep = "https://html.duckduckgo.com/html/"
	}
	c := d.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, strings.NewReader(url.Values{"q": {query}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes))
	if err != nil {
		return nil, err
	}
	return ParseDDGHTML(string(body), max), nil
}

// ParseDDGHTML extracts organic results from a DuckDuckGo HTML results page: each result__a
// anchor (title + link, unwrapping the /l/?uddg= redirector) with the result__snippet that
// follows it. Ads (y.js links) are skipped.
func ParseDDGHTML(page string, max int) []SearchResult {
	anchors := ddgAnchorRE.FindAllStringSubmatchIndex(page, -1)
	var out []SearchResult
	for i, a := range anchors {
		if max > 0 && len(out) >= max {
			break
		}
		tag := page[a[0]:a[2]]
		hm := hrefRE.FindStringSubmatch(tag)
		if hm == nil {
			continue
		}
		link := ddgTarget(html.UnescapeString(hm[1]))
		if link == "" {
			continue
		}
		end := len(page)
		if i+1 < len(anchors) {
			end = anchors[i+1][0]
		}
		snippet := ""
		if sm := ddgSnippetRE.FindStringSubmatch(page[a[1]:end]); sm != nil {
			snippet = htmlText(sm[1])
		}
		out = append(out, SearchResult{Title: htmlText(page[a[2]:a[3]]), URL: link, Snippet: snippet})
	}
	return out
}

func htmlText(s string) string {
	return collapseSpace(html.UnescapeString(tagRE.ReplaceAllString(s, "")))
}

// ddgTarget returns the real result URL ("" for ads and non-http links).
func ddgTarget(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") {
		if strings.HasPrefix(u.Path, "/y.js") {
			return ""
		}
		if t := u.Query().Get("uddg"); t != "" {
			href = t
			if u, err = url.Parse(t); err != nil {
				return ""
			}
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return href
}

// toText decodes a page body as UTF-8, replacing invalid bytes.
func toText(b []byte) string { return strings.ToValidUTF8(string(b), "�") }

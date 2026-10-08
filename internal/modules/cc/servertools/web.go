package servertools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/platform/ssrf"
)

// Web egress for quick_search and deep_research (docs/cc/04 §3.11-3.12, §11 "Web"). The
// SSRF guard is internal/platform/ssrf (the legacy quick_search one, shared with recipes).

// fetchUserAgent is what page scrapes send (legacy quick_search).
const fetchUserAgent = "Mozilla/5.0 (compatible; JarvisBot/1.0)"

// Fetcher is the SSRF-guarded HTTP GET (legacy _safe_get).
type Fetcher = ssrf.Fetcher

// ErrBlockedHost is returned for a URL (or redirect hop) pointing at a private or disallowed host.
var ErrBlockedHost = ssrf.ErrBlockedHost

// IPBlocked is the legacy _ip_blocked.
func IPBlocked(ip netip.Addr) bool { return ssrf.IPBlocked(ip) }

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
	body, err := io.ReadAll(io.LimitReader(resp.Body, ssrf.DefaultMaxBytes))
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

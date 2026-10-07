package servertools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIPBlocked(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.1.2.3", "172.16.5.4", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "224.0.0.1", "240.0.0.1", "255.255.255.255", "198.18.0.1", "192.0.2.5",
		"::1", "::", "fe80::1", "fc00::1", "fd12::1", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"64:ff9b::1.2.3.4", "2001:db8::1",
	}
	for _, s := range blocked {
		if !IPBlocked(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if IPBlocked(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func fakeDNS(m map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		ips, ok := m[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, s := range ips {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

func TestIsBlockedHost(t *testing.T) {
	f := &Fetcher{LookupIP: fakeDNS(map[string][]string{
		"public.test":  {"93.184.216.34"},
		"mixed.test":   {"93.184.216.34", "10.0.0.5"},
		"private.test": {"192.168.0.10"},
	})}
	ctx := context.Background()
	cases := map[string]bool{
		"": true, "localhost": true, "LOCALHOST.": true, "127.0.0.1": true, "[::1]": true, "::1": true,
		"169.254.169.254": true, "10.0.0.1": true, "public.test": false, "mixed.test": true,
		"private.test": true, "unresolvable.test": true, "8.8.8.8": false,
	}
	for host, want := range cases {
		if got := f.IsBlockedHost(ctx, host); got != want {
			t.Errorf("IsBlockedHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// loopbackOK lets tests reach httptest servers while everything else stays guarded.
func loopbackOK() *Fetcher {
	return &Fetcher{Blocked: func(ip netip.Addr) bool { return !ip.Unmap().IsLoopback() && IPBlocked(ip) }}
}

func TestFetcherBlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	if _, _, err := (&Fetcher{}).Get(context.Background(), srv.URL, nil); !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("default fetcher reached loopback: %v", err)
	}
	// The dial hook alone also refuses (DNS rebinding defence).
	_, err := (&Fetcher{}).client().Get(srv.URL)
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("dialer allowed loopback: %v", err)
	}
	for _, u := range []string{"ftp://example.com/x", "file:///etc/passwd", "http:///nohost"} {
		if _, _, err := (&Fetcher{}).Get(context.Background(), u, nil); !errors.Is(err, errInvalidURL) {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestFetcherRedirects(t *testing.T) {
	var target string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-private":
			http.Redirect(w, r, "http://10.0.0.1/admin", http.StatusFound)
		case "/to-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusMovedPermanently)
		case "/to-v6-loop":
			http.Redirect(w, r, "http://[::2]/", http.StatusTemporaryRedirect)
		case "/hop":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/final":
			target = r.Header.Get("Authorization")
			w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	f := loopbackOK()
	ctx := context.Background()
	for _, p := range []string{"/to-private", "/to-metadata", "/to-v6-loop"} {
		if _, _, err := f.Get(ctx, srv.URL+p, nil); !errors.Is(err, ErrBlockedHost) {
			t.Errorf("%s: want blocked, got %v", p, err)
		}
	}
	st, body, err := f.Get(ctx, srv.URL+"/hop", http.Header{"Authorization": {"x"}})
	if err != nil || st != 200 || string(body) != "ok" || target != "x" {
		t.Fatalf("same-host redirect: %d %q %v auth=%q", st, body, err, target)
	}
	if _, _, err := f.Get(ctx, srv.URL+"/loop", nil); !errors.Is(err, errTooManyRedirects) {
		t.Fatalf("loop: %v", err)
	}
}

func TestExtractText(t *testing.T) {
	in := "<html><head><style>p{}</style><SCRIPT type=x>var a=1;</SCRIPT></head>" +
		"<body><p>Fish &amp; chips&nbsp;&lt;3</p>\n\n<div>&quot;hi&quot; it&#39;s</div></body></html>"
	if got := ExtractText(in); got != `Fish & chips <3 "hi" it's` {
		t.Fatalf("got %q", got)
	}
	if got := pageTitle("<title>\n A &amp; B </title>"); got != "A & B" {
		t.Fatalf("title %q", got)
	}
}

const ddgFixture = `<div class="results">
<div class="result results_links results_links_deep result--ad">
 <a rel="nofollow" class="result__a" href="https://duckduckgo.com/y.js?ad_domain=x.com&amp;u3=1">Ad title</a>
 <a class="result__snippet" href="#">buy now</a>
</div>
<div class="result results_links results_links_deep web-result">
 <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fmars%3Fa%3D1&amp;rut=abc">Mars <b>mission</b> news</a></h2>
 <a class="result__snippet" href="//duckduckgo.com/l/?uddg=x">The <b>rover</b> landed &amp; drove.</a>
</div>
<div class="result results_links web-result">
 <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://nasa.gov/mars">NASA Mars</a></h2>
</div>
<div class="result web-result"><a class="result__a" href="https://third.example/">Third</a>
<a class="result__snippet">three</a></div>
</div>`

func TestParseDDGHTML(t *testing.T) {
	got := ParseDDGHTML(ddgFixture, 2)
	want := []SearchResult{
		{Title: "Mars mission news", URL: "https://example.com/mars?a=1", Snippet: "The rover landed & drove."},
		{Title: "NASA Mars", URL: "https://nasa.gov/mars", Snippet: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v want %+v", i, got[i], want[i])
		}
	}
	if all := ParseDDGHTML(ddgFixture, 0); len(all) != 3 || all[2].Snippet != "three" {
		t.Fatalf("all: %+v", all)
	}
}

func TestDuckDuckGoSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("q") != "mars rover" {
			http.Error(w, "bad", 400)
			return
		}
		w.Write([]byte(ddgFixture))
	}))
	defer srv.Close()
	res, err := (&DuckDuckGo{Endpoint: srv.URL}).Search(context.Background(), "mars rover", 2)
	if err != nil || len(res) != 2 {
		t.Fatalf("%v %+v", err, res)
	}
}

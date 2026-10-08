package ssrf

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

func TestFetcherBlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	if _, _, err := (&Fetcher{}).Get(context.Background(), srv.URL, nil); !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("default fetcher reached loopback: %v", err)
	}
	// The dial hook alone also refuses (DNS rebinding defence).
	_, err := (&Fetcher{}).Client().Get(srv.URL)
	if !errors.Is(err, ErrBlockedHost) {
		t.Fatalf("dialer allowed loopback: %v", err)
	}
	for _, u := range []string{"ftp://example.com/x", "file:///etc/passwd", "http:///nohost"} {
		if _, _, err := (&Fetcher{}).Get(context.Background(), u, nil); !errors.Is(err, ErrInvalidURL) {
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
	if _, _, err := f.Get(ctx, srv.URL+"/loop", nil); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("loop: %v", err)
	}
}


// loopbackOK lets tests reach httptest servers while everything else stays guarded.
func loopbackOK() *Fetcher {
	return &Fetcher{Blocked: func(ip netip.Addr) bool { return !ip.Unmap().IsLoopback() && IPBlocked(ip) }}
}

func TestCrossOriginDropsCredentials(t *testing.T) {
	var gotAuth, gotCookie string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		w.Write([]byte("ok"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusFound) // same host, another port: another origin
	}))
	defer srv.Close()
	r, err := loopbackOK().Do(context.Background(), http.MethodGet, srv.URL, http.Header{"Authorization": {"a"}, "Cookie": {"c"}, "User-Agent": {"ua"}})
	if err != nil || r.Status != 200 || gotAuth != "" || gotCookie != "" || r.URL != other.URL+"/x" {
		t.Fatalf("cross-origin hop kept credentials: %+v %v auth=%q cookie=%q", r, err, gotAuth, gotCookie)
	}
}

func TestHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}))
	defer srv.Close()
	r, err := loopbackOK().Do(context.Background(), http.MethodHead, srv.URL, nil)
	if err != nil || r.Status != 200 || r.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("head: %+v %v", r, err)
	}
}

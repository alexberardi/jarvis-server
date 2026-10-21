package node

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeAddressTakesWhateverIsPasted(t *testing.T) {
	ok := map[string]string{
		"192.168.1.10":                       "http://192.168.1.10:7700",
		" jarvis.local ":                     "http://jarvis.local:7700",
		"jarvis.local:7700":                  "http://jarvis.local:7700",
		"http://10.0.0.5:7700/":              "http://10.0.0.5:7700",
		"https://jarvis.example":             "https://jarvis.example",
		"http://[fe80::1]:7700":              "http://[fe80::1]:7700",
		"https://j.example:8443/":            "https://j.example:8443",
		"http://192.0.2.7:7710":              "http://192.0.2.7:7710", // the web app: resolved later
		"http://192.0.2.7:7710/addons?tab=1": "http://192.0.2.7:7710",
		"192.0.2.7:7710/settings#voice":      "http://192.0.2.7:7710",
		"http://jarvis.local/admin/nodes":    "http://jarvis.local:7700",
		"HTTP://Jarvis.Local:7710/":          "http://Jarvis.Local:7710",
		"http://h:7700/cc":                   "http://h:7700", // a mounted module's URL
	}
	for in, want := range ok {
		if got, err := NormalizeAddress(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "ftp://x", "http://u:p@host", "file:///etc/passwd", "javascript:alert(1)", "http://"} {
		if got, err := NormalizeAddress(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

// fakeServer is one jarvisd: the config service and the web app, on two ports of 127.0.0.1.
func fakeServer(t *testing.T) (config, web *httptest.Server) {
	t.Helper()
	config = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			_, _ = w.Write([]byte(`{"service":"jarvis-config-service"}`))
		case "/services":
			_, _ = w.Write([]byte(`{"services":[{"name":"jarvis-command-center","url":"http://127.0.0.1:1"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	// The web app answers every path with its page (an SPA), /info and /services included.
	web = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html><body>Jarvis Automation</body></html>"))
	}))
	t.Cleanup(config.Close)
	t.Cleanup(web.Close)
	return config, web
}

func portOf(t *testing.T, s *httptest.Server) string {
	u, _ := url.Parse(s.URL)
	_, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveConfigURLFromAnyAddressOfTheServer(t *testing.T) {
	config, web := fakeServer(t)
	cp := portOf(t, config)
	for _, typed := range []string{
		config.URL,                      // the config service itself
		config.URL + "/",                // with a slash
		"127.0.0.1",                     // a bare host: the config service's port
		"127.0.0.1:" + cp,               // host:port
		web.URL,                         // the web app (7710): the same host's config port
		web.URL + "/addons?household=1", // any page of it
		strings.TrimPrefix(web.URL, "http://") + "/settings",
	} {
		got, err := ResolveConfigURLOn(context.Background(), http.DefaultClient, typed, cp)
		if err != nil || got != config.URL {
			t.Errorf("%q: %q %v, want %q", typed, got, err, config.URL)
		}
	}
}

func TestResolveConfigURLSaysWhatIsWrongInPlainWords(t *testing.T) {
	_, web := fakeServer(t)
	// Only a web page answers (the config port has nothing): not a Jarvis Automation server.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadPort := portOf(t, dead)
	dead.Close()
	_, err := ResolveConfigURLOn(context.Background(), http.DefaultClient, web.URL+"/x", deadPort)
	var nj *NotJarvisError
	if !errors.As(err, &nj) || !strings.Contains(err.Error(), "doesn't look like a Jarvis Automation server") {
		t.Fatalf("web page only: %v", err)
	}
	// Nothing answers at all.
	_, err = ResolveConfigURLOn(context.Background(), http.DefaultClient, "127.0.0.1:"+deadPort, deadPort)
	var na *NoAnswerError
	if !errors.As(err, &na) || !strings.Contains(err.Error(), "No Jarvis Automation server answered") {
		t.Fatalf("nothing there: %v", err)
	}
	// Not an address.
	if _, err := ResolveConfigURLOn(context.Background(), http.DefaultClient, "ftp://x", deadPort); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("ftp: %v", err)
	}
	for _, e := range []error{nj, na} {
		if strings.Contains(e.Error(), "invalid character") || strings.Contains(e.Error(), "json") {
			t.Fatalf("not plain words: %v", e)
		}
	}
}

func TestDiscoverOnAWebPageSaysItIsNotJarvis(t *testing.T) {
	_, web := fakeServer(t)
	_, err := Discover(context.Background(), http.DefaultClient, web.URL)
	var nj *NotJarvisError
	if !errors.As(err, &nj) || strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("Discover on HTML: %v", err)
	}
}

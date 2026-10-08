package servertools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// loopbackOK lets tests reach httptest servers while everything else stays guarded.
func loopbackOK() *Fetcher {
	return &Fetcher{Blocked: func(ip netip.Addr) bool { return !ip.Unmap().IsLoopback() && IPBlocked(ip) }}
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

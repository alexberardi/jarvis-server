// Package livetest names the cloud providers the live tests run against (llm's
// TestRemoteLive, cc's TestCloudLive): each one whose JARVIS_TEST_<NAME>_KEY is set, with
// JARVIS_TEST_<NAME>_MODEL and _URL overriding its default model and URL (a gateway). The endpoint is resolved the way a
// remote label's is (engine.NormalizeBaseURL, engine.RemoteAPI with auto), so a test sees
// what a user who pasted the provider's URL gets. CI: .github/workflows/cloud-live.yml.
package livetest

import (
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

// Provider is one cloud endpoint to test.
type Provider struct {
	Name, BaseURL, API, Key, Model string
}

var providers = []struct{ name, env, url, model string }{
	{"anthropic", "ANTHROPIC", "https://api.anthropic.com", "claude-haiku-4-5-20251001"},
	{"openai", "OPENAI", "https://api.openai.com/v1", "gpt-5-nano"},
	{"gemini", "GEMINI", "https://generativelanguage.googleapis.com", "gemini-3.1-flash-lite"},
}

// Providers are the providers with a key set, in a fixed order.
func Providers() []Provider {
	var out []Provider
	for _, p := range providers {
		key := os.Getenv("JARVIS_TEST_" + p.env + "_KEY")
		if key == "" {
			continue
		}
		model := p.model
		if m := os.Getenv("JARVIS_TEST_" + p.env + "_MODEL"); m != "" {
			model = m
		}
		url := p.url
		if u := os.Getenv("JARVIS_TEST_" + p.env + "_URL"); u != "" {
			url = u
		}
		out = append(out, Provider{Name: p.name, BaseURL: engine.NormalizeBaseURL(url),
			API: engine.RemoteAPI(engine.APIAuto, url), Key: key, Model: model})
	}
	return out
}

// Busy remembers the provider's last HTTP status, so a check that fails while the provider
// is still refusing after jarvisd's retries (429, 5xx: Google's "experiencing high demand")
// skips instead: that is the provider's capacity, not a request it rejected.
type Busy struct{ last atomic.Int32 }

// Client is the HTTP client for the llm.Service under test.
func (b *Busy) Client() *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err == nil {
			b.last.Store(int32(resp.StatusCode))
		}
		return resp, err
	})}
}

// Fatalf fails the test, or skips it when the provider's last answer was 429 or 5xx.
func (b *Busy) Fatalf(t testing.TB, format string, args ...any) {
	t.Helper()
	if s := b.last.Load(); s == http.StatusTooManyRequests || s >= 500 {
		t.Skipf("provider overloaded (HTTP %d after retries), skipped: "+format, append([]any{s}, args...)...)
	}
	t.Fatalf(format, args...)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

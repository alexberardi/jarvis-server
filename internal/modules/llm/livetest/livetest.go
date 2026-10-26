// Package livetest names the cloud providers the live tests run against (llm's
// TestRemoteLive, cc's TestCloudLive): each one whose JARVIS_TEST_<NAME>_KEY is set, with
// JARVIS_TEST_<NAME>_MODEL and _URL overriding its default model and URL (a gateway). The endpoint is resolved the way a
// remote label's is (engine.NormalizeBaseURL, engine.RemoteAPI with auto), so a test sees
// what a user who pasted the provider's URL gets. CI: .github/workflows/cloud-live.yml.
package livetest

import (
	"os"

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

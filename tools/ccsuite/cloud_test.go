package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cloud key comes from the environment first, then from the cloud env file; the model and
// URL default to Sonnet 5.5 on api.anthropic.com.
func TestCloudConfigSources(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cloud.env")
	if err := os.WriteFile(file, []byte("JARVIS_TEST_ANTHROPIC_KEY=sk-file\nJARVIS_TEST_ANTHROPIC_MODEL=claude-haiku-5-5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		env             map[string]string
		key, model, url string
		wantErr         bool
	}{
		{"none", map[string]string{"JARVIS_CLOUD_ENV": filepath.Join(dir, "missing.env")}, "", "", "", true},
		{"env", map[string]string{"JARVIS_CLOUD_ENV": filepath.Join(dir, "missing.env"), "JARVIS_TEST_ANTHROPIC_KEY": "sk-env"},
			"sk-env", "claude-sonnet-5-5", "https://api.anthropic.com", false},
		{"file", map[string]string{"JARVIS_CLOUD_ENV": file}, "sk-file", "claude-haiku-5-5", "https://api.anthropic.com", false},
		{"env beats file", map[string]string{"JARVIS_CLOUD_ENV": file, "JARVIS_TEST_ANTHROPIC_KEY": "sk-env",
			"JARVIS_TEST_ANTHROPIC_URL": "https://gw.example/v1"}, "sk-env", "claude-haiku-5-5", "https://gw.example/v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"JARVIS_CLOUD_ENV", "JARVIS_TEST_ANTHROPIC_KEY", "JARVIS_TEST_ANTHROPIC_MODEL", "JARVIS_TEST_ANTHROPIC_URL"} {
				t.Setenv(k, tc.env[k])
			}
			c, err := cloudConfig()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("no error, got %+v", c)
				}
				if strings.Contains(err.Error(), "sk-") {
					t.Fatalf("error names a key: %v", err)
				}
				return
			}
			if err != nil || c.Key != tc.key || c.Model != tc.model || c.URL != tc.url {
				t.Fatalf("got %+v, %v", c, err)
			}
		})
	}
}

// The labels body points live, background (errands plan on it by default) and cloud at the provider with vision on; its String never
// shows the key.
func TestCloudLabelsBody(t *testing.T) {
	c := cloud{Key: "sk-secret", Model: "claude-sonnet-5-5", URL: "https://api.anthropic.com"}
	b, _ := json.Marshal(c.labels())
	var got map[string]map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"live", "background", "cloud"} {
		v := got[l]
		if v["engine"] != "remote" || v["remote_url"] != c.URL || v["remote_model"] != c.Model ||
			v["remote_api_key"] != c.Key || v["remote_vision"] != true || v["remote_api"] != "auto" {
			t.Errorf("%s: %v", l, v)
		}
	}
	if s := c.String(); strings.Contains(s, "sk-secret") || !strings.Contains(s, "claude-sonnet-5-5") {
		t.Errorf("String: %q", s)
	}
}

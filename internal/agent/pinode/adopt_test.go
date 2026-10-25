package pinode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// pythonNode copies testdata/pynode (pynode-gen.py's provisioned Python node) into a temp dir.
func pythonNode(t *testing.T) PythonNode {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS("testdata/pynode")); err != nil {
		t.Fatal(err)
	}
	return PythonNode{InstallDir: filepath.Join(dst, "opt-jarvis-node"), SecretDir: filepath.Join(dst, "home-pi-.jarvis")}
}

// withVenv puts a .venv/bin/python in the node's install dir: an interpreter with sqlcipher3
// and cryptography (JARVIS_ADOPT_PYTHON, which CI sets on Linux; skipped without one).
func withVenv(t *testing.T, p PythonNode) {
	t.Helper()
	py := os.Getenv("JARVIS_ADOPT_PYTHON")
	if runtime.GOOS == "windows" {
		t.Skip("the Pi node is Linux")
	}
	if py == "" {
		py = "python3"
		if exec.Command(py, "-I", "-c", "import sqlcipher3, cryptography").Run() != nil {
			t.Skip("no python3 with sqlcipher3 and cryptography (set JARVIS_ADOPT_PYTHON)")
		}
	}
	bin := filepath.Join(p.InstallDir, ".venv", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "python"), []byte("#!/bin/sh\nexec '"+py+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// sums hashes every file under the Python node's dirs (but the test's own .venv).
func sums(t *testing.T, p PythonNode) map[string][32]byte {
	t.Helper()
	m := map[string][32]byte{}
	for _, root := range []string{p.InstallDir, p.SecretDir} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				if d != nil && d.Name() == ".venv" {
					return filepath.SkipDir
				}
				return err
			}
			b, err := os.ReadFile(path)
			m[path] = sha256.Sum256(b)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestAdoptPythonNode(t *testing.T) {
	p := pythonNode(t)
	withVenv(t, p)
	before := sums(t, p)
	creds := filepath.Join(t.TempDir(), "jarvis-agent", "node.json")

	c, err := p.Adopt(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	if c.NodeID != "pi-kitchen-7f3a" || c.NodeKey != "test-node-key-0123456789" || c.ConfigURL != "http://192.0.2.10:7700" || c.Room != "kitchen" {
		t.Fatalf("creds: %+v", c)
	}
	got, err := node.LoadCreds(creds) // also refuses a node.json others can read
	if err != nil || got.APIKey() != "pi-kitchen-7f3a:test-node-key-0123456789" {
		t.Fatalf("node.json: %+v %v", got, err)
	}

	st, err := nodesettings.OpenStore(filepath.Dir(creds), c.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	alice := int64(7)
	if st.Secret("OPENWEATHER_API_KEY", "integration", nil) != "weather-secret" || st.Secret("SPOTIFY_TOKEN", "user", &alice) != "alice-token" {
		t.Fatal("command secrets not carried over")
	}
	if st.Enabled("get_sports_scores") || !st.Enabled("get_weather") {
		t.Fatal("disabled commands not carried over")
	}
	r, ok := st.Data("set_reminder", "r1", time.Now())
	if !ok || r.Value("text") != "take the bins out" || r.Value("due_at") != "2030-01-01T19:00:00+00:00" {
		t.Fatalf("reminder: %v %v", r, ok)
	}
	if _, ok := st.Data("set_timer", "t1", time.Now()); ok {
		t.Fatal("a timer belongs in timers.json, not command data")
	}
	if !bytes.Equal(st.Key(), func() []byte {
		b := make([]byte, 32)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}()) {
		t.Fatal("K2 not carried over")
	}

	var ts []timers.Timer
	b, err := os.ReadFile(filepath.Join(filepath.Dir(creds), "timers.json"))
	if err == nil {
		err = json.Unmarshal(b, &ts)
	}
	if err != nil || len(ts) != 1 || ts[0].ID != "t1" || *ts[0].Label != "pasta" || !ts[0].Ends.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("timers.json: %v %v", ts, err)
	}

	after := sums(t, p)
	for f, h := range before {
		if after[f] != h {
			t.Errorf("%s changed", f)
		}
	}
	if len(after) != len(before) {
		t.Errorf("files appeared in the Python node's dirs: %d before, %d after", len(before), len(after))
	}

	// Once adopted, node.json is the agent's own and is never replaced.
	if _, err := p.Adopt(context.Background(), creds); !errors.Is(err, ErrEnrolled) {
		t.Fatalf("second adoption: %v", err)
	}
}

func TestAdoptExistingNodeJSONWins(t *testing.T) {
	p := pythonNode(t)
	creds := filepath.Join(t.TempDir(), "node.json")
	own := node.Creds{ConfigURL: "http://jarvisd:7700", NodeID: "go-node", NodeKey: "k"}
	if err := node.SaveCreds(creds, own); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Adopt(context.Background(), creds); !errors.Is(err, ErrEnrolled) {
		t.Fatalf("got %v, want ErrEnrolled", err)
	}
	if c, err := node.LoadCreds(creds); err != nil || c.NodeID != "go-node" {
		t.Fatalf("node.json replaced: %+v %v", c, err)
	}
}

func TestAdoptOnlyAProvisionedNode(t *testing.T) {
	for name, change := range map[string]func(PythonNode){
		"placeholders": func(p PythonNode) {
			_ = os.WriteFile(filepath.Join(p.InstallDir, "config.json"),
				[]byte(`{"node_id":"your-node-id","api_key":"your_api_key_here","jarvis_config_service_url":"http://x:7700"}`), 0o644)
		},
		"empty key": func(p PythonNode) {
			_ = os.WriteFile(filepath.Join(p.InstallDir, "config.json"), []byte(`{"node_id":"n1","api_key":""}`), 0o644)
		},
		"no marker":    func(p PythonNode) { _ = os.Remove(filepath.Join(p.SecretDir, ".provisioned")) },
		"no config":    func(p PythonNode) { _ = os.Remove(filepath.Join(p.InstallDir, "config.json")) },
		"no secretdir": func(p PythonNode) { _ = os.RemoveAll(p.SecretDir) },
	} {
		t.Run(name, func(t *testing.T) {
			p := pythonNode(t)
			change(p)
			creds := filepath.Join(t.TempDir(), "node.json")
			if _, err := p.Adopt(context.Background(), creds); !errors.Is(err, ErrNotProvisioned) {
				t.Fatalf("got %v, want ErrNotProvisioned", err)
			}
			if _, err := os.Stat(creds); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("node.json written")
			}
		})
	}
}

// A database the agent can't read leaves it unenrolled: no half adoption.
func TestAdoptFailsWholeWithoutTheVenv(t *testing.T) {
	p := pythonNode(t)
	creds := filepath.Join(t.TempDir(), "node.json")
	if _, err := p.Adopt(context.Background(), creds); err == nil || errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("got %v, want an export error", err)
	}
	if _, err := os.Stat(creds); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("node.json written")
	}
}

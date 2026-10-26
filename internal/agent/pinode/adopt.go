package pinode

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// The Go node takes over a provisioned Python node's identity instead of enrolling a new one
// (decisions/2026-10-23-pi-node-adopts-python-identity.md): the same node id and key, so jarvisd
// keeps the node's room, settings, devices, history and app pairing. Its local state (command
// secrets, disabled commands, command data such as reminders, timers and K2) comes over once.
// Every Python file is only read: going back to the Python node (install-pi.sh --keep-python)
// finds it as it was.

//go:embed pyexport.py
var pyExport string

// PythonNode is where a jarvis-node-setup install keeps its identity and state.
type PythonNode struct {
	InstallDir string // /opt/jarvis-node: config.json, jarvis_node.db, .venv
	SecretDir  string // $JARVIS_SECRET_DIRECTORY or ~/.jarvis: .provisioned, db.key, secrets.key (K1), k2.enc
}

// DefaultPythonNode is install.sh's layout for the user running the agent (the same as the
// Python node's: the units share User= and HOME=).
func DefaultPythonNode() (PythonNode, error) {
	p := PythonNode{InstallDir: "/opt/jarvis-node", SecretDir: os.Getenv("JARVIS_SECRET_DIRECTORY")}
	if p.SecretDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return p, err
		}
		p.SecretDir = filepath.Join(home, ".jarvis")
	}
	return p, nil
}

// config.json's placeholders (N:config.example.json, N:provisioning/factory_reset.py).
var placeholders = map[string]bool{"your-node-id": true, "your_api_key_here": true}

// ErrNotProvisioned: there is no provisioned Python node to adopt.
var ErrNotProvisioned = errors.New("no provisioned Python node")

// ErrEnrolled: the agent has its own node.json already, which is never replaced.
var ErrEnrolled = errors.New("this agent is enrolled already")

// Identity is the Python node's credentials, or ErrNotProvisioned: no .provisioned marker, or a
// config.json without a real node id and key.
func (p PythonNode) Identity() (node.Creds, error) {
	var c node.Creds
	if _, err := os.Stat(filepath.Join(p.SecretDir, ".provisioned")); err != nil {
		return c, ErrNotProvisioned
	}
	b, err := os.ReadFile(filepath.Join(p.InstallDir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotProvisioned
	}
	if err != nil {
		return c, err
	}
	var cfg struct {
		NodeID    string `json:"node_id"`
		APIKey    string `json:"api_key"`
		ConfigURL string `json:"jarvis_config_service_url"`
		Room      string `json:"room"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return c, fmt.Errorf("the Python node's config.json: %w", err)
	}
	if cfg.NodeID == "" || cfg.APIKey == "" || placeholders[cfg.NodeID] || placeholders[cfg.APIKey] {
		return c, ErrNotProvisioned
	}
	if cfg.ConfigURL == "" {
		return c, errors.New("the Python node's config.json has no jarvis_config_service_url")
	}
	return node.Creds{ConfigURL: cfg.ConfigURL, NodeID: cfg.NodeID, NodeKey: cfg.APIKey, Room: cfg.Room, EnrolledAt: time.Now().UTC()}, nil
}

// pyState is pyexport.py's output.
type pyState struct {
	Secrets []struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		Scope     string `json:"scope"`
		ValueType string `json:"value_type"`
		UserID    *int64 `json:"user_id"`
	} `json:"secrets"`
	Disabled    []string `json:"disabled_commands"`
	CommandData []struct {
		Command string              `json:"command_name"`
		Key     string              `json:"data_key"`
		Data    nodesettings.Object `json:"data"`
		Expires *time.Time          `json:"expires_at"`
	} `json:"command_data"`
	K2 *nodesettings.K2 `json:"k2"`
}

// export runs pyexport.py with the Python node's venv: Go has no SQLCipher.
func (p PythonNode) export(ctx context.Context) (pyState, error) {
	var s pyState
	var out, stderr bytes.Buffer
	py := filepath.Join(p.InstallDir, ".venv", "bin", "python")
	cmd := exec.CommandContext(ctx, py, "-I", "-c", pyExport, filepath.Join(p.InstallDir, "jarvis_node.db"), p.SecretDir)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return s, fmt.Errorf("reading the Python node's database with %s: %w: %s", py, err, bytes.TrimSpace(stderr.Bytes()))
	}
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		return s, fmt.Errorf("the Python node's database export: %w", err)
	}
	return s, nil
}

// Adopt makes the provisioned Python node's identity this agent's: its local state goes into
// the agent's config dir (credsPath's directory) and node.json is written last, so a failure
// leaves the agent unenrolled and the next try starts over (every import is an upsert).
// ErrNotProvisioned when there is nothing to adopt, ErrEnrolled when node.json exists (it wins).
func (p PythonNode) Adopt(ctx context.Context, credsPath string) (node.Creds, error) {
	if _, err := os.Stat(credsPath); err == nil {
		return node.Creds{}, ErrEnrolled
	}
	c, err := p.Identity()
	if err != nil {
		return c, err
	}
	s, err := p.export(ctx)
	if err != nil {
		return c, err
	}
	dir := filepath.Dir(credsPath)
	st, err := nodesettings.OpenStore(dir, c.NodeID)
	if err != nil {
		return c, err
	}
	for _, x := range s.Secrets {
		if err := st.SetSecret(x.Key, x.Value, x.Scope, x.ValueType, x.UserID); err != nil {
			return c, err
		}
	}
	for _, cmd := range s.Disabled {
		if err := st.SetEnabled(cmd, false); err != nil {
			return c, err
		}
	}
	var ts []timers.Timer
	for _, d := range s.CommandData {
		if d.Command != "set_timer" {
			if err := st.SaveData(d.Command, d.Key, d.Data, d.Expires); err != nil {
				return c, err
			}
			continue
		}
		// The Go node's timers are the host's (timers.json), not command data.
		b, _ := json.Marshal(d.Data)
		var t timers.Timer
		if err := json.Unmarshal(b, &t); err != nil {
			return c, fmt.Errorf("the Python node's timer %s: %w", d.Key, err)
		}
		ts = append(ts, t)
	}
	if len(ts) > 0 {
		if err := writeFile(filepath.Join(dir, "timers.json"), ts); err != nil {
			return c, err
		}
	}
	if s.K2 != nil {
		if err := st.SaveK2(s.K2.K2, s.K2.Kid, s.K2.CreatedAt); err != nil {
			return c, err
		}
	}
	return c, node.SaveCreds(credsPath, c)
}

func writeFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

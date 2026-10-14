// Package sidecar is a spike of the Python package sidecar: the Go agent starts one Python
// child, kept warm, that loads IJarvisCommand packages and answers JSON-lines requests over
// stdin/stdout. Go keeps the node identity, key and MQTT; the child only ever sees tool calls.
//
// Storage (JarvisStorage's secrets and command data) is answered from the agent's settings
// store over the same pipes (DESIGN §16): the child asks between a request and its reply.
//
// Not built yet (see docs/agent/DESIGN.md): uv-managed interpreter and venv, package install,
// restart-on-crash, per-call cancellation, manifest tags.
package sidecar

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// Storage is the agent's settings store as the child's StorageBackend reaches it
// (*nodesettings.Store implements it).
type Storage interface {
	Secret(key, scope string, uid *int64) string
	SetSecret(key, value, scope, valueType string, uid *int64) error
	DeleteSecret(key, scope string, uid *int64) error
	SaveData(command, key string, data map[string]any, expires *time.Time) error
	Data(command, key string, now time.Time) map[string]any
	AllData(command string, now time.Time) []map[string]any
	DeleteData(command, key string) (bool, error)
	DeleteAllData(command string) (int, error)
}

//go:embed jarvis_sidecar.py
var hostScript []byte

// Sidecar is one running Python child. Requests are serialised: the child handles one at a
// time, which is enough for a spike and matches how a Pi node runs commands.
type Sidecar struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Scanner
	log   *slog.Logger
	dir   string

	mu      sync.Mutex
	nextID  int
	tools   []node.ToolDef
	secrets map[string][]nodesettings.SecretSpec
	store   Storage
	now     func() time.Time
}

type request struct {
	ID     int            `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

type reply struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	// From is "child" on a reverse request (the child's storage asking the agent).
	From   string          `json:"from"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Start launches python with the embedded host script over commandsDir and loads the tool list.
// st answers the commands' storage calls; nil leaves them unanswered (a secret reads as unset).
func Start(ctx context.Context, python, commandsDir string, log *slog.Logger, st Storage) (*Sidecar, error) {
	dir, err := os.MkdirTemp("", "jarvis-sidecar-")
	if err != nil {
		return nil, err
	}
	script := filepath.Join(dir, "jarvis_sidecar.py")
	if err := os.WriteFile(script, hostScript, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	// Not CommandContext: the child lives as long as the Sidecar, and Close ends it.
	cmd := exec.Command(python, "-u", script, commandsDir)
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	go func() { // the child's stderr is its log
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Info("sidecar", "line", sc.Text())
		}
	}()
	out := bufio.NewScanner(stdout)
	out.Buffer(make([]byte, 64<<10), 16<<20)
	s := &Sidecar{cmd: cmd, stdin: stdin, out: out, log: log, dir: dir, store: st, now: time.Now,
		secrets: map[string][]nodesettings.SecretSpec{}}
	var desc struct {
		Tools []struct {
			Name       string                    `json:"name"`
			ClientTool map[string]any            `json:"client_tool"`
			Command    map[string]any            `json:"command"`
			Secrets    []nodesettings.SecretSpec `json:"secrets"`
		} `json:"tools"`
	}
	if err := s.roundTrip("describe", nil, &desc); err != nil {
		s.Close()
		return nil, fmt.Errorf("describe: %w", err)
	}
	for _, t := range desc.Tools {
		s.tools = append(s.tools, node.ToolDef{Name: t.Name, ClientTool: t.ClientTool, Command: t.Command})
		s.secrets[t.Name] = t.Secrets
	}
	return s, nil
}

// Commands are the sidecar's commands with their declared secrets, for the settings snapshot.
func (s *Sidecar) Commands() []nodesettings.Command {
	out := make([]nodesettings.Command, 0, len(s.tools))
	for _, t := range s.tools {
		desc, _ := t.Command["description"].(string)
		out = append(out, nodesettings.Command{Name: t.Name, Description: desc, Secrets: s.secrets[t.Name]})
	}
	return out
}

func (s *Sidecar) roundTrip(method string, params map[string]any, out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := s.nextID
	b, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	if _, err := s.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("sidecar gone: %w", err)
	}
	var r reply
	for {
		if !s.out.Scan() {
			if err := s.out.Err(); err != nil {
				return err
			}
			return errors.New("sidecar exited")
		}
		r = reply{}
		if err := json.Unmarshal(s.out.Bytes(), &r); err != nil {
			return fmt.Errorf("bad sidecar reply: %w", err)
		}
		if r.From != "child" {
			break
		}
		// The child's storage asking, mid-request: answer and keep waiting for the reply.
		if err := s.answer(r); err != nil {
			return fmt.Errorf("sidecar gone: %w", err)
		}
	}
	if r.ID != id {
		return fmt.Errorf("sidecar reply id %d, want %d", r.ID, id)
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	return json.Unmarshal(r.Result, out)
}

// Available is the tool list loaded at start (packages don't change under a running sidecar).
func (s *Sidecar) Available(context.Context) []node.ToolDef { return s.tools }

// Call forwards one tool call. The context's deadline is not yet propagated to the child.
func (s *Sidecar) Call(_ context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	known := false
	for _, t := range s.tools {
		known = known || t.Name == name
	}
	if !known {
		return nil, false
	}
	r := map[string]any{"tool_call_id": req.ToolCallID, "voice_command": req.VoiceCommand}
	if req.UserID != nil {
		r["user_id"] = *req.UserID
	}
	var res struct {
		Output  map[string]any `json:"output"`
		Signals *struct {
			WaitForInput       bool   `json:"wait_for_input"`
			ClearHistory       bool   `json:"clear_history"`
			OnResponseComplete string `json:"on_response_complete"`
		} `json:"signals"`
	}
	if err := s.roundTrip("call", map[string]any{"command_name": name, "arguments": args, "request": r}, &res); err != nil {
		return node.Fail("sidecar: %v", err), true
	}
	if res.Output == nil {
		return node.Fail("sidecar returned no output"), true
	}
	if sg := res.Signals; sg != nil {
		sig := node.Signals{WaitForInput: sg.WaitForInput, ClearHistory: sg.ClearHistory}
		if token := sg.OnResponseComplete; token != "" {
			sig.OnResponseComplete = func() error {
				var done struct {
					Ran bool `json:"ran"`
				}
				if err := s.roundTrip("complete", map[string]any{"token": token}, &done); err != nil {
					return err
				}
				if !done.Ran {
					return fmt.Errorf("sidecar no longer holds %s's deferred step", name)
				}
				return nil
			}
		}
		res.Output[node.SignalsKey] = sig
	}
	return res.Output, true
}

// Close ends the child (closing stdin lets it exit cleanly) and removes its script.
func (s *Sidecar) Close() error {
	_ = s.stdin.Close()
	err := s.cmd.Wait()
	os.RemoveAll(s.dir)
	return err
}

// storageParams are the fields of a child's storage request.
type storageParams struct {
	Command   string         `json:"command"`
	Key       string         `json:"key"`
	Data      map[string]any `json:"data"`
	ExpiresAt *string        `json:"expires_at"`
	Scope     string         `json:"scope"`
	Value     *string        `json:"value"`
	ValueType string         `json:"value_type"`
	UserID    *int64         `json:"user_id"`
}

// answer serves one reverse request from the child.
func (s *Sidecar) answer(q reply) error {
	result, err := s.storage(q.Method, q.Params)
	out := map[string]any{"to": "child", "id": q.ID, "result": result}
	if err != nil {
		s.log.Warn("sidecar: storage call failed", "method", q.Method, "err", err)
		out = map[string]any{"to": "child", "id": q.ID, "error": err.Error()}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

// expiry parses the child's expires_at (Python isoformat; a naive time is local, as the node's).
func expiry(p *string) (*time.Time, error) {
	if p == nil || *p == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, *p, time.Local); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("bad expires_at %q", *p)
}

func (s *Sidecar) storage(method string, raw json.RawMessage) (any, error) {
	if s.store == nil {
		return nil, errors.New("no storage on this agent")
	}
	var p storageParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	switch method {
	case "storage.get_secret":
		if v := s.store.Secret(p.Key, p.Scope, p.UserID); v != "" {
			return v, nil
		}
		return nil, nil // Python None: unset
	case "storage.set_secret":
		if p.Value == nil {
			return nil, errors.New("set_secret without a value")
		}
		return nil, s.store.SetSecret(p.Key, *p.Value, p.Scope, p.ValueType, p.UserID)
	case "storage.delete_secret":
		return nil, s.store.DeleteSecret(p.Key, p.Scope, p.UserID)
	case "storage.save":
		exp, err := expiry(p.ExpiresAt)
		if err != nil {
			return nil, err
		}
		return nil, s.store.SaveData(p.Command, p.Key, p.Data, exp)
	case "storage.get":
		if d := s.store.Data(p.Command, p.Key, s.now()); d != nil {
			return d, nil
		}
		return nil, nil
	case "storage.get_all":
		return s.store.AllData(p.Command, s.now()), nil
	case "storage.delete":
		return s.store.DeleteData(p.Command, p.Key)
	case "storage.delete_all":
		return s.store.DeleteAllData(p.Command)
	}
	return nil, fmt.Errorf("unknown storage method %q", method)
}

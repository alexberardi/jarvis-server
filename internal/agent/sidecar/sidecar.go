// Package sidecar is a spike of the Python package sidecar: the Go agent starts one Python
// child, kept warm, that loads IJarvisCommand packages and answers JSON-lines requests over
// stdin/stdout. Go keeps the node identity, key and MQTT; the child only ever sees tool calls.
//
// Not built yet (see docs/agent/DESIGN.md): uv-managed interpreter and venv, package install,
// restart-on-crash, per-call cancellation, secrets, manifest tags.
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

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

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

	mu     sync.Mutex
	nextID int
	tools  []node.ToolDef
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
}

// Start launches python with the embedded host script over commandsDir and loads the tool list.
func Start(ctx context.Context, python, commandsDir string, log *slog.Logger) (*Sidecar, error) {
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
	s := &Sidecar{cmd: cmd, stdin: stdin, out: out, log: log, dir: dir}
	var desc struct {
		Tools []struct {
			Name       string         `json:"name"`
			ClientTool map[string]any `json:"client_tool"`
			Command    map[string]any `json:"command"`
		} `json:"tools"`
	}
	if err := s.roundTrip("describe", nil, &desc); err != nil {
		s.Close()
		return nil, fmt.Errorf("describe: %w", err)
	}
	for _, t := range desc.Tools {
		s.tools = append(s.tools, node.ToolDef{Name: t.Name, ClientTool: t.ClientTool, Command: t.Command})
	}
	return s, nil
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
	if !s.out.Scan() {
		if err := s.out.Err(); err != nil {
			return err
		}
		return errors.New("sidecar exited")
	}
	var r reply
	if err := json.Unmarshal(s.out.Bytes(), &r); err != nil {
		return fmt.Errorf("bad sidecar reply: %w", err)
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
		Output map[string]any `json:"output"`
	}
	if err := s.roundTrip("call", map[string]any{"command_name": name, "arguments": args, "request": r}, &res); err != nil {
		return node.Fail("sidecar: %v", err), true
	}
	if res.Output == nil {
		return node.Fail("sidecar returned no output"), true
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

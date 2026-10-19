// Package sidecar is the Python package sidecar: the Go agent starts one Python child, kept
// warm, that loads IJarvisCommand packages and answers JSON-lines requests over stdin/stdout.
// Go keeps the node identity, key and MQTT; the child only ever sees tool calls. The packages
// are the ones internal/agent/packages installed (a commands .json file), or for the spike's
// --sidecar-commands, the modules in a directory. Live is the one in service.
//
// Storage (JarvisStorage's secrets and command data) is answered from the agent's settings
// store over the same pipes (DESIGN §16): the child asks between a request and its reply.
// With a Host (the Pi profile), the SDK's other backends ask the same way.
//
// Not built yet (see docs/agent/DESIGN.md): restart-on-crash, per-call cancellation.
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
	"strings"
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
	SaveData(command, key string, data nodesettings.Object, expires *time.Time) error
	Data(command, key string, now time.Time) (nodesettings.Object, bool)
	Records(command string, now time.Time) []nodesettings.Record
	DeleteData(command, key string) (bool, error)
	DeleteAllData(command string) (int, error)
}

// Host answers the child's SDK backend requests other than storage (llm.*, cc.*, timer.*,
// audio.*, alerts.*, inbox.*, signals.*: docs/agent/go-pi-node.md §5.2). The result is
// JSON-encoded back to the child; an error reaches the command as an exception, which the
// SDK's helpers turn into their safe default.
type Host interface {
	Answer(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// Takeover is a Host that takes over the speaker for a music command (sdkhost.Host on a Pi):
// BeforeCall runs before every call, with the command's declared takes_over_playback_binary
// ("" when it declares none), and stops the other players when the call is a "play".
type Takeover interface {
	BeforeCall(ctx context.Context, command, declared string, args map[string]any)
}

// Options are what a sidecar's commands reach in the agent. Storage nil leaves storage calls
// unanswered (a secret reads as unset); Host nil registers no other backend in the child.
type Options struct {
	Storage Storage
	Host    Host
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
	// logged closes when the child's stderr is read to the end.
	logged chan struct{}

	mu      sync.Mutex
	nextID  int
	tools   []node.ToolDef
	secrets map[string][]nodesettings.SecretSpec
	extras  map[string]extras
	browser []nodesettings.DataCommand
	pkgOf   map[string]string // command -> the installed package it came from
	player  map[string]string // command -> its takes_over_playback_binary
	failed  map[string]string // package -> why its commands didn't load
	store   Storage
	host    Host
	now     func() time.Time
}

// extras are the snapshot fields a package may declare beyond its secrets.
type extras struct {
	AssociatedService string          `json:"associated_service"`
	SetupGuide        json.RawMessage `json:"setup_guide"`
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

// Start launches python with the embedded host script over commandsDir (a directory of modules,
// or an installed-packages .json file) and loads the tool list.
// st answers the commands' storage calls; nil leaves them unanswered (a secret reads as unset).
func Start(ctx context.Context, python, commandsDir string, log *slog.Logger, st Storage) (*Sidecar, error) {
	return StartWith(ctx, python, commandsDir, log, Options{Storage: st})
}

// StartWith is Start with the Pi's backends: o.Host set registers them in the child.
func StartWith(ctx context.Context, python, commandsDir string, log *slog.Logger, o Options) (*Sidecar, error) {
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
	args := []string{"-u", script, commandsDir}
	if o.Host != nil {
		args = append(args, "--backends")
	}
	cmd := exec.Command(python, args...)
	// UTF-8 both ways whatever the platform's locale (Windows' Python would speak cp1252).
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1", "PYTHONIOENCODING=utf-8")
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
	logged := make(chan struct{})
	go func() { // the child's stderr is its log
		defer close(logged)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Info("sidecar", "line", sc.Text())
		}
	}()
	out := bufio.NewScanner(stdout)
	out.Buffer(make([]byte, 64<<10), 16<<20)
	s := &Sidecar{cmd: cmd, stdin: stdin, out: out, log: log, logged: logged, dir: dir, store: o.Storage, host: o.Host, now: time.Now,
		secrets: map[string][]nodesettings.SecretSpec{}, extras: map[string]extras{}, pkgOf: map[string]string{},
		player: map[string]string{}}
	var desc struct {
		Tools []struct {
			Name        string                    `json:"name"`
			ClientTool  map[string]any            `json:"client_tool"`
			Command     map[string]any            `json:"command"`
			Secrets     []nodesettings.SecretSpec `json:"secrets"`
			DataBrowser *nodesettings.DataCommand `json:"data_browser"`
			Package     string                    `json:"package"`
			Player      string                    `json:"takes_over_playback_binary"`
			extras
		} `json:"tools"`
		Failed map[string]string `json:"failed"`
	}
	if err := s.roundTrip("describe", nil, &desc); err != nil {
		s.Close()
		return nil, fmt.Errorf("describe: %w", err)
	}
	for _, t := range desc.Tools {
		s.tools = append(s.tools, node.ToolDef{Name: t.Name, ClientTool: t.ClientTool, Command: t.Command})
		s.secrets[t.Name] = t.Secrets
		s.extras[t.Name] = t.extras
		if t.Package != "" {
			s.pkgOf[t.Name] = t.Package
		}
		if t.Player != "" {
			s.player[t.Name] = t.Player
		}
		if b := t.DataBrowser; b != nil { // nil: its settings couldn't be read, so it stays out
			b.Name = t.Name
			s.browser = append(s.browser, *b)
		}
	}
	s.failed = desc.Failed
	return s, nil
}

// Failed are the installed packages whose commands didn't load, with why.
func (s *Sidecar) Failed() map[string]string { return s.failed }

// Package is the installed package a command came from ("" for none).
func (s *Sidecar) Package(command string) string { return s.pkgOf[command] }

// Commands are the sidecar's commands with their declared secrets, for the settings snapshot.
func (s *Sidecar) Commands() []nodesettings.Command {
	out := make([]nodesettings.Command, 0, len(s.tools))
	for _, t := range s.tools {
		desc, _ := t.Command["description"].(string)
		x := s.extras[t.Name]
		out = append(out, nodesettings.Command{Name: t.Name, Description: desc, Secrets: s.secrets[t.Name],
			AssociatedService: x.AssociatedService, SetupGuide: x.SetupGuide})
	}
	return out
}

// DataCommands are the commands' mobile data-browser settings, read once at start.
func (s *Sidecar) DataCommands() []nodesettings.DataCommand { return s.browser }

// Summaries runs each record through its command's display_summary in one round trip.
func (s *Sidecar) Summaries(_ context.Context, command string, records []nodesettings.Object) ([]map[string]any, error) {
	var res struct {
		Summaries []map[string]any `json:"summaries"`
	}
	if err := s.roundTrip("data_summaries", map[string]any{"command_name": command, "records": records}, &res); err != nil {
		return nil, err
	}
	return res.Summaries, nil
}

// Create runs the command's data_browser_create, which stamps uid as the owner and saves the
// record through JarvisStorage (back into the agent's store, mid-request).
func (s *Sidecar) Create(_ context.Context, command string, values nodesettings.Object, uid json.Number) (string, json.RawMessage, error) {
	var who any // JSON null for no user: the command refuses it
	if uid != "" {
		who = uid
	}
	var res struct {
		Key      string          `json:"key"`
		Record   json.RawMessage `json:"record"`
		Rejected *string         `json:"rejected"`
		Failed   *string         `json:"failed"`
	}
	err := s.roundTrip("data_create", map[string]any{"command_name": command, "fields": values, "requesting_user_id": who}, &res)
	switch {
	case err != nil:
		return "", nil, err
	case res.Rejected != nil:
		return "", nil, &nodesettings.ErrRejected{Message: *res.Rejected}
	case res.Failed != nil:
		return "", nil, errors.New(*res.Failed)
	}
	return res.Key, res.Record, nil
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
func (s *Sidecar) Call(ctx context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	known := false
	for _, t := range s.tools {
		known = known || t.Name == name
	}
	if !known {
		return nil, false
	}
	if tk, ok := s.host.(Takeover); ok {
		tk.BeforeCall(ctx, name, s.player[name], args)
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
	// Wait closes the stderr pipe, so read the child's last lines first; bounded, as a
	// process the child left behind may hold the pipe open.
	select {
	case <-s.logged:
	case <-time.After(2 * time.Second):
	}
	err := s.cmd.Wait()
	os.RemoveAll(s.dir)
	return err
}

// storageParams are the fields of a child's storage request.
type storageParams struct {
	Command   string              `json:"command"`
	Key       string              `json:"key"`
	Data      nodesettings.Object `json:"data"`
	ExpiresAt *string             `json:"expires_at"`
	Scope     string              `json:"scope"`
	Value     *string             `json:"value"`
	ValueType string              `json:"value_type"`
	UserID    *int64              `json:"user_id"`
}

// answer serves one reverse request from the child.
func (s *Sidecar) answer(q reply) error {
	var result any
	var err error
	switch {
	case strings.HasPrefix(q.Method, "storage."):
		result, err = s.storage(q.Method, q.Params)
	case s.host != nil:
		result, err = s.host.Answer(context.Background(), q.Method, q.Params)
	default:
		err = fmt.Errorf("unknown method %q", q.Method)
	}
	out := map[string]any{"to": "child", "id": q.ID, "result": result}
	if err != nil {
		s.log.Warn("sidecar: backend call failed", "method", q.Method, "err", err)
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

// isoformat is Python's datetime.isoformat() of t in UTC (microseconds only when non-zero).
func isoformat(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond()/1000 != 0 {
		return t.Format("2006-01-02T15:04:05.000000+00:00")
	}
	return t.Format("2006-01-02T15:04:05+00:00")
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
		if d, ok := s.store.Data(p.Command, p.Key, s.now()); ok {
			return d, nil
		}
		return nil, nil
	case "storage.get_all":
		// As the Pi's CommandDataRepository.get_all: each record with its key and expiry.
		out := []nodesettings.Object{}
		for _, r := range s.store.Records(p.Command, s.now()) {
			d := r.Data
			key, _ := json.Marshal(r.Key)
			d.Set("_data_key", key)
			exp := json.RawMessage("null")
			if r.ExpiresAt != nil {
				exp, _ = json.Marshal(isoformat(*r.ExpiresAt))
			}
			d.Set("_expires_at", exp)
			out = append(out, d)
		}
		return out, nil
	case "storage.delete":
		return s.store.DeleteData(p.Command, p.Key)
	case "storage.delete_all":
		return s.store.DeleteAllData(p.Command)
	}
	return nil, fmt.Errorf("unknown storage method %q", method)
}

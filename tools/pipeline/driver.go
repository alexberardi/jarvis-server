package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxContinues bounds the tool round-trips of one turn, like a node would.
const maxContinues = 6

// CallLog is one tool call made during a turn.
type CallLog struct {
	Name   string         `json:"name"`
	Args   map[string]any `json:"args"`
	Result any            `json:"result,omitempty"` // the node's answer, or what the server tool returned (serverCallLogs)
}

// TurnLog is everything one turn did.
type TurnLog struct {
	Say         string     `json:"say"`
	Voice       *VoiceLog  `json:"voice,omitempty"` // a voiced turn: what STT heard (sent instead of Say)
	NodeCalls   []CallLog  `json:"node_calls"`
	ServerCalls []CallLog  `json:"server_calls"` // model calls command-center ran itself (from the tap)
	Reply       string     `json:"reply"`
	Stop        string     `json:"stop"`
	Seconds     float64    `json:"seconds"`
	End         time.Time  `json:"end"` // when the turn finished ({now} in reply expectations)
	LLM         []TapEntry `json:"llm,omitempty"`
	Error       string     `json:"error,omitempty"`
	Failures    []string   `json:"failures,omitempty"`
}

// ScenarioLog is one run of a scenario.
type ScenarioLog struct {
	Name           string    `json:"name"`
	File           string    `json:"file"`
	ConversationID string    `json:"conversation_id"`
	Turns          []TurnLog `json:"turns"`
	Pass           bool      `json:"pass"`
}

// Driver runs conversations as the node.
type Driver struct {
	Node   *Node
	TapLog string // the sandbox's llm.jsonl; "" = no model-call capture
	Root   string // repo root, for the golden tool sets
	// Primary is the seeded superuser's id, what a scenario's "speaker": "primary" means.
	Primary int64
	// Voice voices every scenario (say/run --voice); VoiceName overrides a scenario's voice.
	Voice     bool
	VoiceName string
	// Voices renders named voices (the admin's preview); nil = the house voice only.
	Voices *AdminVoice
	// VoiceReady: the sandbox has TTS and STT (up --voice). run skips voice scenarios without.
	VoiceReady bool
	// Desktop makes the primary's desktop agent for "node": "desktop" scenarios (desktop.go);
	// desktop caches it once made.
	Desktop *DesktopSetup
	desktop map[string]*Node // by owner ("" the primary, "removed")
}

// speakerID resolves a scenario's speaker.
func (d *Driver) speakerID(spec string) (int64, error) {
	switch spec {
	case "":
		return 0, nil
	case "primary":
		if d.Primary == 0 {
			return 0, fmt.Errorf(`speaker "primary": the sandbox exports no SBX_USER_ID (scripts/sandbox up again)`)
		}
		return d.Primary, nil
	}
	id, err := strconv.ParseInt(spec, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("speaker %q: want \"primary\" or a user id", spec)
	}
	return id, nil
}

// Run plays a scenario and checks every turn, as the scenario's node.
func (d *Driver) Run(ctx context.Context, sc Scenario) ScenarioLog {
	n, err := d.nodeFor(ctx, sc.Node, sc.Owner)
	if err != nil {
		say := ""
		if len(sc.Turns) > 0 {
			say = sc.Turns[0].Say
		}
		return ScenarioLog{Name: sc.Name, File: sc.File, Turns: []TurnLog{{Say: say, Error: "start: " + err.Error()}}}
	}
	if n == d.Node {
		return d.run(ctx, sc)
	}
	as := *d
	as.Node = n
	return as.run(ctx, sc)
}

func (d *Driver) run(ctx context.Context, sc Scenario) (out ScenarioLog) {
	out = ScenarioLog{Name: sc.Name, File: sc.File, ConversationID: newUUID(), Pass: true}
	spec := resolveToolSpec(sc)
	tools, err := LoadToolSet(d.Root, spec)
	speaker, serr := d.speakerID(sc.Speaker)
	if err == nil {
		err = serr
	}
	home := sc.Home
	if home == "" && sc.Node == "desktop" {
		home = "none" // a desktop agent sends no devices (internal/agent/voice nodeContext)
	}
	if home != "" && home != "none" && sc.File != "" && (strings.ContainsRune(home, '/') || strings.HasSuffix(home, ".json")) && !filepath.IsAbs(home) {
		home = filepath.Join(filepath.Dir(sc.File), home)
	}
	agents, herr := LoadHome(d.Root, home)
	if err == nil {
		err = herr
	}
	vocab, verr := scenarioVocabulary(d.Root, sc, spec)
	if err == nil {
		err = verr
	}
	if err == nil && sc.FreshMemories {
		err = d.clearMemories(ctx)
	}
	if err == nil && len(sc.Settings) > 0 {
		var restore func(context.Context) error
		if restore, err = d.applySettings(ctx, sc.Settings); err == nil {
			defer func() {
				if rerr := restore(context.WithoutCancel(ctx)); rerr != nil && len(out.Turns) > 0 {
					last := &out.Turns[len(out.Turns)-1]
					last.Failures = append(last.Failures, rerr.Error())
					out.Pass = false
				}
			}()
		}
	}
	if err == nil {
		tz := sc.Timezone
		if tz == "" {
			tz = "America/New_York"
		}
		err = d.Node.Start(ctx, out.ConversationID, tz, tools, agents, vocab)
	}
	if err != nil {
		out.Pass = false
		out.Turns = []TurnLog{{Say: sc.Turns[0].Say, Error: "start: " + err.Error()}}
		return out
	}
	nodeNames := toolNames(tools)
	defer d.Node.End(context.WithoutCancel(ctx), out.ConversationID)
	voice := sc.VoiceName
	if d.VoiceName != "" {
		voice = d.VoiceName
	}
	voiced := sc.Voice || d.Voice || d.VoiceName != ""
	for _, turn := range sc.Turns {
		tl := d.turn(ctx, out.ConversationID, nodeNames, turn.Say, voiced, voice, speaker, func(name string) any { return sc.resultFor(turn, name) })
		tl.Failures = turn.Expect.Check(tl)
		if len(tl.Failures) > 0 {
			out.Pass = false
		}
		out.Turns = append(out.Turns, tl)
		if tl.Error != "" {
			break
		}
	}
	return out
}

// turn sends one utterance and answers tool calls until command-center is done. A voiced turn
// sends what STT heard of it instead.
func (d *Driver) turn(ctx context.Context, cid string, nodeNames []string, say string, voiced bool, voice string, speaker int64, result func(string) any) (tl TurnLog) {
	tl.Say = say
	mark := tapSize(d.TapLog)
	start := time.Now()
	defer func() {
		tl.End = time.Now()
		tl.Seconds = tl.End.Sub(start).Seconds()
		tl.LLM = readTap(d.TapLog, mark)
		tl.ServerCalls = serverCallLogs(tl.LLM, nodeNames)
	}()
	text := say
	if voiced {
		v, err := d.hear(ctx, cid, say, voice)
		tl.Voice = v
		if err == nil && v.Heard == "" {
			err = errors.New("stt: heard nothing")
		}
		if err != nil {
			tl.Error = err.Error()
			return tl
		}
		text = v.Heard
	}
	resp, err := d.Node.Command(ctx, cid, text, speaker)
	for i := 0; err == nil && resp.StopReason == "tool_calls" && len(resp.ToolCalls) > 0; i++ {
		if i == maxContinues {
			err = fmt.Errorf("still calling tools after %d round-trips", maxContinues)
			break
		}
		var results []ToolResult
		for _, c := range resp.ToolCalls {
			r := result(c.Function.Name)
			tl.NodeCalls = append(tl.NodeCalls, CallLog{Name: c.Function.Name, Args: c.Args(), Result: r})
			results = append(results, ToolResult{ToolCallID: c.ID, Output: r})
		}
		resp, err = d.Node.Continue(ctx, cid, results)
	}
	if err != nil {
		tl.Error = err.Error()
		return tl
	}
	tl.Stop = resp.StopReason
	switch {
	case resp.StopReason == "error":
		var cmds []struct {
			Errors *struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(resp.Commands, &cmds)
		tl.Error = "command-center: stop_reason error"
		if len(cmds) > 0 && cmds[0].Errors != nil {
			tl.Error = "command-center: " + cmds[0].Errors.Message
		}
	case resp.AssistantMessage != nil:
		tl.Reply = *resp.AssistantMessage
	case resp.ValidationRequest != nil:
		tl.Reply = resp.ValidationRequest.Question
	}
	return tl
}

// serverCallLogs are the model calls in the tapped entries that command-center ran itself (not
// in the node's set), each with its result as the next request shows it to the model
// (serverResults). A text-path fast path speaks the results' messages with no further model
// call, so its results are not seen (nil).
func serverCallLogs(entries []TapEntry, nodeNames []string) []CallLog {
	var out []CallLog
	for i, e := range entries {
		var calls []ToolCall
		for _, c := range e.Reply.Calls() {
			if !slices.Contains(nodeNames, c.Function.Name) {
				calls = append(calls, c)
			}
		}
		results := serverResults(entries[i+1:], calls)
		for k, c := range calls {
			out = append(out, CallLog{Name: c.Function.Name, Args: c.Args(), Result: results[k]})
		}
	}
	return out
}

// textResultsHeaders open the text path's results prompt (textfilter TextModeFormatPrompt,
// ContinueToolResultsMessage): the results follow the next blank line, one per line.
var textResultsHeaders = []string{"Tool results below.", "Here are the tool results."}

// serverResults are the calls' results in the first later request that has messages. Native:
// the role=tool message with the call's id, else (a text-path model's calls are parsed with
// fresh ids) the last one of its name after that request's last assistant message. Text path:
// the lines of the results prompt in its last user message, in call order. JSON is decoded.
func serverResults(later []TapEntry, calls []ToolCall) []any {
	out := make([]any, len(calls))
	for _, e := range later {
		var req struct {
			Messages []struct {
				Role       string          `json:"role"`
				ToolCallID string          `json:"tool_call_id"`
				Name       string          `json:"name"`
				Content    json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(e.Request, &req) != nil || len(req.Messages) == 0 {
			continue
		}
		type toolMsg struct {
			id, name string
			content  json.RawMessage
		}
		var all, recent []toolMsg // every role=tool message; those after the last assistant message
		for _, m := range req.Messages {
			switch m.Role {
			case "assistant":
				recent = nil
			case "tool":
				t := toolMsg{m.ToolCallID, m.Name, m.Content}
				all, recent = append(all, t), append(recent, t)
			}
		}
		found := false
		used := make([]bool, len(recent))
		for k, c := range calls {
			if i := slices.IndexFunc(all, func(t toolMsg) bool { return c.ID != "" && t.id == c.ID }); i >= 0 {
				out[k], found = decodeContent(all[i].content), true
				continue
			}
			for i, t := range recent {
				if !used[i] && t.name == c.Function.Name {
					used[i], out[k], found = true, decodeContent(t.content), true
					break
				}
			}
		}
		if !found {
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if m := req.Messages[i]; m.Role == "user" {
					text, _ := decodeContent(m.Content).(string)
					for k, line := range textResults(text) {
						if k < len(out) {
							out[k] = decodeContent(mustJSON(line))
						}
					}
					break
				}
			}
		}
		return out
	}
	return out
}

// textResults are the result lines of a text-path results prompt, nil when text isn't one.
func textResults(text string) []string {
	for _, h := range textResultsHeaders {
		i := strings.Index(text, h)
		if i < 0 {
			continue
		}
		j := strings.Index(text[i:], "\n\n")
		if j < 0 {
			return nil
		}
		body := strings.TrimSuffix(strings.TrimSpace(text[i+j+2:]), "/no_think")
		return strings.Split(strings.TrimSpace(body), "\n")
	}
	return nil
}

func mustJSON(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// decodeContent is a message's content (a string, or text parts): JSON text decoded, other
// text as is.
func decodeContent(raw json.RawMessage) any {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) != nil {
			var v any
			_ = json.Unmarshal(raw, &v)
			return v
		}
		for _, p := range parts {
			s += p.Text
		}
	}
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}

func toolNames(tools []json.RawMessage) []string {
	var names []string
	for _, t := range tools {
		var d struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(t, &d) == nil && d.Function.Name != "" {
			names = append(names, d.Function.Name)
		}
	}
	return names
}

func tapSize(path string) int64 {
	if path == "" {
		return 0
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// readTap returns the entries appended to the tap log after offset.
func readTap(path string, offset int64) []TapEntry {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		return nil
	}
	var out []TapEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var e TapEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

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
	Result any            `json:"result,omitempty"` // the node's answer (node tools only)
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
		for _, e := range tl.LLM {
			for _, c := range e.Reply.Calls() {
				if !slices.Contains(nodeNames, c.Function.Name) {
					tl.ServerCalls = append(tl.ServerCalls, CallLog{Name: c.Function.Name, Args: c.Args()})
				}
			}
		}
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

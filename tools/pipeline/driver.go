package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
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
	NodeCalls   []CallLog  `json:"node_calls"`
	ServerCalls []CallLog  `json:"server_calls"` // model calls command-center ran itself (from the tap)
	Reply       string     `json:"reply"`
	Stop        string     `json:"stop"`
	Seconds     float64    `json:"seconds"`
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

// Run plays a scenario and checks every turn.
func (d *Driver) Run(ctx context.Context, sc Scenario) ScenarioLog {
	out := ScenarioLog{Name: sc.Name, File: sc.File, ConversationID: newUUID(), Pass: true}
	spec := sc.Tools
	if spec != "" && spec != "node" && spec != "client" && spec != "none" && sc.File != "" && !filepath.IsAbs(spec) {
		spec = filepath.Join(filepath.Dir(sc.File), spec)
	}
	tools, err := LoadToolSet(d.Root, spec)
	speaker, serr := d.speakerID(sc.Speaker)
	if err == nil {
		err = serr
	}
	home := sc.Home
	if home != "" && home != "none" && sc.File != "" && (strings.ContainsRune(home, '/') || strings.HasSuffix(home, ".json")) && !filepath.IsAbs(home) {
		home = filepath.Join(filepath.Dir(sc.File), home)
	}
	agents, herr := LoadHome(d.Root, home)
	if err == nil {
		err = herr
	}
	if err == nil {
		tz := sc.Timezone
		if tz == "" {
			tz = "America/New_York"
		}
		err = d.Node.Start(ctx, out.ConversationID, tz, tools, agents)
	}
	if err != nil {
		out.Pass = false
		out.Turns = []TurnLog{{Say: sc.Turns[0].Say, Error: "start: " + err.Error()}}
		return out
	}
	nodeNames := toolNames(tools)
	defer d.Node.End(context.WithoutCancel(ctx), out.ConversationID)
	for _, turn := range sc.Turns {
		tl := d.turn(ctx, out.ConversationID, nodeNames, turn.Say, speaker, func(name string) any { return sc.resultFor(turn, name) })
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

// turn sends one utterance and answers tool calls until command-center is done.
func (d *Driver) turn(ctx context.Context, cid string, nodeNames []string, say string, speaker int64, result func(string) any) (tl TurnLog) {
	tl.Say = say
	mark := tapSize(d.TapLog)
	start := time.Now()
	defer func() {
		tl.Seconds = time.Since(start).Seconds()
		tl.LLM = readTap(d.TapLog, mark)
		for _, e := range tl.LLM {
			for _, c := range e.Reply.ToolCalls {
				if !slices.Contains(nodeNames, c.Function.Name) {
					tl.ServerCalls = append(tl.ServerCalls, CallLog{Name: c.Function.Name, Args: c.Args()})
				}
			}
		}
	}()
	resp, err := d.Node.Command(ctx, cid, say, speaker)
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

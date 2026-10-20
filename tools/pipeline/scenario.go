package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// A scenario is a scripted conversation: what the user says each turn, what the node answers
// for each tool call, and what must hold afterwards. Files are JSON (scenarios/*.json); a file
// holds one scenario object or an array of them.
type Scenario struct {
	Name string   `json:"name"`
	Tags []string `json:"tags,omitempty"`
	// Tools is the node's tool set: "node" (default, a real Pi node's commands), "client",
	// "none", or a path to a JSON array of OpenAI tool definitions (relative to the file).
	Tools    string `json:"tools,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	// Speaker is who the voice match says is talking: "" nobody (an unidentified voice),
	// "primary" the sandbox's seeded superuser, or a user id. Memory tools need one (D21).
	Speaker string `json:"speaker,omitempty"`
	// Node is who plays the turns: "" the seeded kitchen Pi, "desktop" a desktop agent the
	// primary user signed in on, whose voice turns act as that owner with no voice match
	// (desktop.go). Leave Speaker empty to test the owner rather than a speaker hint.
	Node string `json:"node,omitempty"`
	// Owner, for "node": "desktop": "" the primary is its owner; "removed" its owner is a
	// member the primary then removed from the household, so the agent has no usable owner
	// and per-user things say to sign in again (desktop.go).
	Owner string `json:"owner,omitempty"`
	// AlsoHeardBy is a second node in earshot ("desktop", or "" for the seeded kitchen Pi
	// when Node is "desktop"): it opens its own conversation with the same tools, and every
	// turn is sent by both at once as a wake turn (turn_source: wake), as when one
	// "Hey Jarvis" wakes two devices. The turn's log is the node that answered; the others
	// are in its "others" (wake arbitration, loop #287). Unset: one node.
	AlsoHeardBy *string `json:"also_heard_by,omitempty"`
	// Home is the device payload the node reports at /conversation/start (node_context.agents,
	// what a Pi with Home Assistant sends): a name under tools/pipeline/homes ("avery", the
	// default, matches the seeded rooms), "none", or a JSON file path.
	Home string `json:"home,omitempty"`
	// STTVocabulary is node_context.stt_vocabulary: the app names a desktop agent sends at
	// /conversation/start (open windows first, then its catalogue; apps.Vocabulary) for
	// whisper's prompt on its voice commands. Unset, a desktop scenario (the desktop toolset
	// or "node": "desktop") sends a typical agent's (toolsets/desktop.apps.json, vocabulary.go)
	// and any other sends none; "" sends none.
	STTVocabulary *string `json:"stt_vocabulary,omitempty"`
	// Voice plays every turn through the sandbox's TTS and STT (a sandbox up with --voice):
	// the transcript is what command-center gets. VoiceName is a Kokoro voice to say it in
	// ("" = the house voice). say/run --voice turns it on for every scenario.
	Voice     bool   `json:"voice,omitempty"`
	VoiceName string `json:"voice_name,omitempty"`
	// FreshMemories deletes the primary user's own memories before each run (memories.go), so
	// a memory scenario doesn't see what an earlier run stored.
	FreshMemories bool `json:"fresh_memories,omitempty"`
	// Settings are household settings the scenario needs (e.g. {"watches.enabled": true}): the
	// driver puts them as the primary household admin before the conversation starts and puts
	// the earlier values back afterwards (settings.go).
	Settings map[string]any `json:"settings,omitempty"`
	// Skip, when set, is why the scenario cannot run yet; it is reported, not played.
	Skip string `json:"skip,omitempty"`
	// Results is the node's output per tool name for every call in the scenario; a turn's own
	// results win. A tool with no result answers {"success": true}.
	Results map[string]any `json:"results,omitempty"`
	Turns   []Turn         `json:"turns"`

	File string `json:"-"`
}

type Turn struct {
	Say     string         `json:"say"`
	Results map[string]any `json:"results,omitempty"`
	Expect  Expect         `json:"expect"`
}

// Expect is checked against one turn. Every set field must hold.
type Expect struct {
	Tool       string            `json:"tool,omitempty"`        // this node tool is called
	AnyTool    []string          `json:"any_tool,omitempty"`    // one of these tools (node or server) is called
	NoTool     bool              `json:"no_tool,omitempty"`     // no node tool is called
	NotTool    []string          `json:"not_tool,omitempty"`    // none of these (node or server) is called
	ServerTool string            `json:"server_tool,omitempty"` // this server tool is called
	Args       map[string]any    `json:"args,omitempty"`        // on the Tool/ServerTool call: these keys equal these values
	ArgsMatch  map[string]string `json:"args_match,omitempty"`  // on that call: each value's text matches the regexp
	// ResultMatch: on that call, each field of its result (the node's answer, or what the server
	// tool returned) matches the regexp, e.g. {"status": "^cancelled$"}; NotResultMatch: none
	// does (a missing field doesn't). With several calls of the tool, the expectation holds when
	// one call meets all of it (args and result), and is reported on the last call otherwise.
	ResultMatch    map[string]string `json:"result_match,omitempty"`
	NotResultMatch map[string]string `json:"not_result_match,omitempty"`
	// NotCall: no call of this tool (node or server) has args matching all of these regexps,
	// e.g. {"tool": "desktop_open", "args_match": {"target": "(?i)type"}}.
	NotCall  *CallMatch `json:"not_call,omitempty"`
	Reply    string     `json:"reply,omitempty"`     // the spoken reply matches this regexp
	NotReply string     `json:"not_reply,omitempty"` // and does not match this one
	Stop     string     `json:"stop,omitempty"`      // final stop_reason
	// Answers: with also_heard_by, how many of the nodes answered (the rest stood down).
	Answers    *int    `json:"answers,omitempty"`
	MaxSeconds float64 `json:"max_seconds,omitempty"` // the turn finished within this
	// Voice turns only: what STT made of the said text.
	Heard    string   `json:"heard,omitempty"`     // the transcript matches this regexp
	NotHeard string   `json:"not_heard,omitempty"` // and does not match this one
	MaxWER   *float64 `json:"max_wer,omitempty"`   // word error rate against the said text ≤ this (0 = word for word)
}

// CallMatch is a tool name and a regexp per argument.
type CallMatch struct {
	Tool      string            `json:"tool"`
	ArgsMatch map[string]string `json:"args_match,omitempty"`
}

// matches reports whether c is this tool with every listed argument matching.
func (m CallMatch) matches(c CallLog) bool {
	if c.Name != m.Tool {
		return false
	}
	for k, re := range m.ArgsMatch {
		v, ok := c.Args[k]
		if !ok || !regexp.MustCompile(re).MatchString(valueText(v)) {
			return false
		}
	}
	return true
}

// hearing reports whether the expectation is about the transcript.
func (e Expect) hearing() bool { return e.Heard != "" || e.NotHeard != "" || e.MaxWER != nil }

// LoadScenarios reads every *.json under the paths (files or directories), sorted by path.
func LoadScenarios(paths []string) ([]Scenario, error) {
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json") {
				files = append(files, path)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	var out []Scenario
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var list []Scenario
		if t := strings.TrimSpace(string(b)); strings.HasPrefix(t, "[") {
			err = json.Unmarshal(b, &list)
		} else {
			var one Scenario
			err = json.Unmarshal(b, &one)
			list = []Scenario{one}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for i := range list {
			list[i].File = f
			if err := list[i].validate(); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", f, list[i].Name, err)
			}
		}
		out = append(out, list...)
	}
	return out, nil
}

func (s Scenario) validate() error {
	if s.Name == "" {
		return fmt.Errorf("scenario without a name")
	}
	if len(s.Turns) == 0 {
		return fmt.Errorf("no turns")
	}
	for i, t := range s.Turns {
		if t.Say == "" {
			return fmt.Errorf("turn %d: empty say", i+1)
		}
		for _, re := range []string{t.Expect.Reply, t.Expect.NotReply, t.Expect.Heard, t.Expect.NotHeard} {
			if _, err := regexp.Compile(re); err != nil {
				return fmt.Errorf("turn %d: %w", i+1, err)
			}
		}
		if nc := t.Expect.NotCall; nc != nil {
			if nc.Tool == "" {
				return fmt.Errorf("turn %d: not_call needs a tool", i+1)
			}
			for k, re := range nc.ArgsMatch {
				if _, err := regexp.Compile(re); err != nil {
					return fmt.Errorf("turn %d: not_call args_match %s: %w", i+1, k, err)
				}
			}
		}
		for k, re := range mergedRegexps(t.Expect.ArgsMatch, t.Expect.ResultMatch, t.Expect.NotResultMatch) {
			if _, err := regexp.Compile(re); err != nil {
				return fmt.Errorf("turn %d: %s: %w", i+1, k, err)
			}
		}
		e := t.Expect
		if (len(e.Args) > 0 || len(e.ArgsMatch) > 0 || len(e.ResultMatch) > 0 || len(e.NotResultMatch) > 0) &&
			e.Tool == "" && e.ServerTool == "" {
			return fmt.Errorf("turn %d: args and results need tool or server_tool", i+1)
		}
	}
	return nil
}

// resultFor is the node's output for a call: the turn's, else the scenario's, else success.
func (s Scenario) resultFor(turn Turn, tool string) any {
	if r, ok := turn.Results[tool]; ok {
		return r
	}
	if r, ok := s.Results[tool]; ok {
		return r
	}
	return map[string]any{"success": true}
}

// Check returns what failed in a turn, empty when the expectation holds.
func (e Expect) Check(t TurnLog) []string {
	var fails []string
	if e.hearing() {
		if v := t.Voice; v == nil {
			fails = append(fails, `heard: not a voice turn ("voice": true, or --voice, on a sandbox up with --voice)`)
		} else {
			if e.Heard != "" && !regexp.MustCompile(e.Heard).MatchString(v.Heard) {
				fails = append(fails, fmt.Sprintf("heard %q, want /%s/", v.Heard, e.Heard))
			}
			if e.NotHeard != "" && regexp.MustCompile(e.NotHeard).MatchString(v.Heard) {
				fails = append(fails, fmt.Sprintf("heard %q, matches /%s/", v.Heard, e.NotHeard))
			}
			if e.MaxWER != nil && v.WER > *e.MaxWER {
				fails = append(fails, fmt.Sprintf("heard %q, word error rate %.2f > %.2f", v.Heard, v.WER, *e.MaxWER))
			}
		}
	}
	if t.Error != "" {
		return append(fails, "error: "+t.Error)
	}
	all := append(slices.Clone(t.NodeCalls), t.ServerCalls...)
	names := func(cs []CallLog) []string {
		var n []string
		for _, c := range cs {
			n = append(n, c.Name)
		}
		return n
	}
	// judge is the call expectation's failures on the calls of the tool: none when one of them
	// meets it, else the last one's.
	judge := func(cs []CallLog, name string) (bool, []string) {
		var last []string
		called := false
		for i := len(cs) - 1; i >= 0; i-- {
			if cs[i].Name != name {
				continue
			}
			f := e.callFails(cs[i])
			if len(f) == 0 {
				return true, nil
			}
			if !called {
				last = f
			}
			called = true
		}
		return called, last
	}
	if e.Tool != "" {
		if ok, f := judge(t.NodeCalls, e.Tool); ok {
			fails = append(fails, f...)
		} else {
			fails = append(fails, fmt.Sprintf("node tool %s not called (node: %v, server: %v)", e.Tool, names(t.NodeCalls), names(t.ServerCalls)))
		}
	}
	if e.ServerTool != "" {
		if ok, f := judge(t.ServerCalls, e.ServerTool); ok {
			if e.Tool == "" {
				fails = append(fails, f...)
			}
		} else {
			fails = append(fails, fmt.Sprintf("server tool %s not called (server: %v)", e.ServerTool, names(t.ServerCalls)))
		}
	}
	if len(e.AnyTool) > 0 && !slices.ContainsFunc(all, func(c CallLog) bool { return slices.Contains(e.AnyTool, c.Name) }) {
		fails = append(fails, fmt.Sprintf("none of %v called (called: %v)", e.AnyTool, names(all)))
	}
	if e.NoTool && len(t.NodeCalls) > 0 {
		fails = append(fails, fmt.Sprintf("no node tool wanted, called %v", names(t.NodeCalls)))
	}
	for _, c := range all {
		if slices.Contains(e.NotTool, c.Name) {
			fails = append(fails, "called "+c.Name)
		}
		if e.NotCall != nil && e.NotCall.matches(c) {
			fails = append(fails, fmt.Sprintf("called %s %s", c.Name, jsonText(c.Args)))
		}
	}
	if re := expandNow(e.Reply, t.End); re != "" && !regexp.MustCompile(re).MatchString(t.Reply) {
		fails = append(fails, fmt.Sprintf("reply %q does not match /%s/", t.Reply, re))
	}
	if e.NotReply != "" && regexp.MustCompile(e.NotReply).MatchString(t.Reply) {
		fails = append(fails, fmt.Sprintf("reply %q matches /%s/", t.Reply, e.NotReply))
	}
	if e.Answers != nil {
		if n := t.answers(); n != *e.Answers {
			fails = append(fails, fmt.Sprintf("%d nodes answered, want %d (%s)", n, *e.Answers, t.nodesSummary()))
		}
	}
	if e.Stop != "" && t.Stop != e.Stop {
		fails = append(fails, fmt.Sprintf("stop_reason %s, want %s", t.Stop, e.Stop))
	}
	if e.MaxSeconds > 0 && t.Seconds > e.MaxSeconds {
		fails = append(fails, fmt.Sprintf("took %.1fs, want ≤ %.1fs", t.Seconds, e.MaxSeconds))
	}
	return fails
}

// callFails is what of the args and result expectations a call misses.
func (e Expect) callFails(c CallLog) []string {
	var fails []string
	for _, k := range sortedKeys(e.Args) {
		want := e.Args[k]
		if got, ok := c.Args[k]; !ok || !reflect.DeepEqual(normalize(got), normalize(want)) {
			fails = append(fails, fmt.Sprintf("%s.%s = %s, want %s", c.Name, k, jsonText(c.Args[k]), jsonText(want)))
		}
	}
	for _, k := range sortedKeys(e.ArgsMatch) {
		re := e.ArgsMatch[k]
		v, ok := c.Args[k]
		if !ok || !regexp.MustCompile(re).MatchString(valueText(v)) {
			fails = append(fails, fmt.Sprintf("%s.%s = %s, want /%s/", c.Name, k, jsonText(v), re))
		}
	}
	result, _ := c.Result.(map[string]any)
	for _, k := range sortedKeys(e.ResultMatch) {
		re := e.ResultMatch[k]
		v, ok := result[k]
		if !ok || !regexp.MustCompile(re).MatchString(valueText(v)) {
			fails = append(fails, fmt.Sprintf("%s result %s = %s, want /%s/ (result %s)", c.Name, k, jsonText(v), re, jsonText(c.Result)))
		}
	}
	for _, k := range sortedKeys(e.NotResultMatch) {
		re := e.NotResultMatch[k]
		if v, ok := result[k]; ok && regexp.MustCompile(re).MatchString(valueText(v)) {
			fails = append(fails, fmt.Sprintf("%s result %s = %s, matches /%s/", c.Name, k, jsonText(v), re))
		}
	}
	return fails
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mergedRegexps is the regexp maps under one key space ("args_match label", …), for validation.
func mergedRegexps(argsMatch, resultMatch, notResultMatch map[string]string) map[string]string {
	out := map[string]string{}
	for name, m := range map[string]map[string]string{"args_match": argsMatch, "result_match": resultMatch,
		"not_result_match": notResultMatch} {
		for k, re := range m {
			out[name+" "+k] = re
		}
	}
	return out
}

// normalize round-trips a value through JSON so 12 and 12.0, int and float64, compare equal.
func normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// valueText is a value as a regexp sees it: strings bare, everything else as JSON.
func valueText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return jsonText(v)
}

// expandNow replaces {now} in a reply regexp with the local 12-hour clock time at the end of
// the turn, a minute either way ("12:06|12:05|12:07"), so a scenario can check that a time
// answer is the real one.
func expandNow(re string, at time.Time) string {
	if !strings.Contains(re, "{now}") {
		return re
	}
	if at.IsZero() {
		at = time.Now()
	}
	var alts []string
	for _, d := range []time.Duration{0, -time.Minute, time.Minute} {
		alts = append(alts, at.Add(d).Format("3:04"))
	}
	return strings.ReplaceAll(re, "{now}", "(?:"+strings.Join(alts, "|")+")")
}

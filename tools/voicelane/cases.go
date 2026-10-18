package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
)

// Cases is e2e/voice/cases.json.
type Cases struct {
	Cases []Case `json:"cases"`
}

// Case is one thing the mic hears and what must happen.
type Case struct {
	Name string `json:"name"`
	// Audio is what the mic hears, in order: speech, silence or generated noise.
	Audio []AudioPart `json:"audio"`
	// Model marks a case whose result depends on the model: it runs --repeat times, under the
	// regressions-only rule (docs/dev/e2e.md).
	Model bool `json:"model,omitempty"`
	// Needs is "pipewire" for a case only real-time audio (pipewire or pi) can play (barge-in).
	Needs  string `json:"needs,omitempty"`
	Expect Expect `json:"expect"`
	Line   int    `json:"-"` // 1-based line of its name in the cases file, for the report
}

// AudioPart is one piece of the mic's audio; exactly one of Say, Silence and Noise is set.
type AudioPart struct {
	Say     string  `json:"say,omitempty"`     // spoken by the sandbox's TTS
	Silence float64 `json:"silence,omitempty"` // seconds
	// Noise is generated, never a recording: "pink" (pink noise) or "music" (chords on a beat).
	Noise string  `json:"noise,omitempty"`
	Secs  float64 `json:"secs,omitempty"` // the noise's length
	Gain  float64 `json:"gain,omitempty"` // the noise's peak, 0..1 (default 0.3)
	// Over plays this part over the previous ones, starting this many seconds before their
	// end (speech over music, a wake word over a reply); 0 appends it.
	Over float64 `json:"over,omitempty"`
}

// Expect is what a case checks. Silent: the node must not answer (no turn, no reply audio, no
// model call) and nothing else may be set.
type Expect struct {
	Silent  bool    `json:"silent,omitempty"`
	Outcome string  `json:"outcome,omitempty"`  // the turn's outcome (default spoken)
	Reply   string  `json:"reply,omitempty"`    // regexp on the transcribed reply
	Tool    string  `json:"tool,omitempty"`     // a tool the model called, or the node ran
	MaxSecs float64 `json:"max_secs,omitempty"` // response-time budget
}

// LoadCases reads and checks a cases file.
func LoadCases(path string) ([]Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var cs Cases
	if err := dec.Decode(&cs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(cs.Cases) == 0 {
		return nil, fmt.Errorf("%s: no cases", path)
	}
	seen := map[string]bool{}
	for i := range cs.Cases {
		c := &cs.Cases[i]
		if err := c.check(); err != nil {
			return nil, fmt.Errorf("%s: case %d (%q): %w", path, i+1, c.Name, err)
		}
		if seen[slug(c.Name)] {
			return nil, fmt.Errorf("%s: case %q twice", path, c.Name)
		}
		seen[slug(c.Name)] = true
		c.Line = lineOf(b, `"name": `+strconv.Quote(c.Name))
	}
	return cs.Cases, nil
}

func (c Case) check() error {
	if slug(c.Name) == "" {
		return errors.New("no name")
	}
	if len(c.Audio) == 0 {
		return errors.New("no audio")
	}
	for i, p := range c.Audio {
		n := 0
		if p.Say != "" {
			n++
		}
		if p.Silence > 0 {
			n++
		}
		if p.Noise != "" {
			n++
			if p.Noise != "pink" && p.Noise != "music" {
				return fmt.Errorf("noise %q: pink or music", p.Noise)
			}
			if p.Secs <= 0 {
				return errors.New("noise without secs")
			}
		}
		if n != 1 {
			return errors.New("an audio part needs exactly one of say, silence, noise")
		}
		if p.Over < 0 || (p.Over > 0 && i == 0) {
			return errors.New("over: a positive overlap with an earlier part")
		}
	}
	if c.Needs != "" && c.Needs != "pipewire" {
		return fmt.Errorf("needs %q: only pipewire", c.Needs)
	}
	e := c.Expect
	if e.Silent && (e.Reply != "" || e.Tool != "" || e.Outcome != "" || e.MaxSecs > 0) {
		return errors.New("a silent case expects nothing else")
	}
	if !e.Silent && e.Reply == "" && e.Tool == "" && e.Outcome == "" {
		return errors.New("expects nothing")
	}
	if e.Silent && c.Model {
		return errors.New("a silent case calls no model")
	}
	if e.Reply != "" {
		if _, err := regexp.Compile(e.Reply); err != nil {
			return fmt.Errorf("reply: %w", err)
		}
	}
	return nil
}

func lineOf(b []byte, needle string) int {
	i := bytes.Index(b, []byte(needle))
	if i < 0 {
		return 0
	}
	return bytes.Count(b[:i], []byte("\n")) + 1
}

// slug is a case's id and directory name: lower case, runs of anything else a dash.
func slug(s string) string {
	var b []byte
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b = append(b, byte(r))
			dash = false
		case r >= 'A' && r <= 'Z':
			b = append(b, byte(r-'A'+'a'))
			dash = false
		case !dash && len(b) > 0:
			b = append(b, '-')
			dash = true
		}
	}
	return string(bytes.TrimSuffix(b, []byte("-")))
}

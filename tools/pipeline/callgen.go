package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// pipeline call-gen: variant call scenarios from seeds (callvariant.go). The dimensions come
// from the sampler, the texts from a model (falling back to the seed's templates when what it
// wrote breaks a dimension), the expectations from derive.

func runCallGen(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("call-gen", flag.ContinueOnError)
	seedsGlob := fs.String("seeds", "tools/pipeline/calls/seeds/*.json", "seed files")
	only := fs.String("only", "", "use only these seeds (comma-separated names)")
	count := fs.Int("count", 60, "variants to write")
	rng := fs.Uint64("rng", 1, "sampler seed: the same seeds, count and rng give the same dimensions")
	out := fs.String("out", "", "file to write the variants to (a JSON array `pipeline call` runs)")
	sandbox := fs.String("sandbox", "", "sandbox whose tap the generator model is reached through")
	url := fs.String("url", "", "OpenAI-compatible base URL for the generator instead of the sandbox's tap")
	model := fs.String("model", "background", "model label that writes the texts")
	offline := fs.Bool("offline", false, "write the seeds' templates only (no model)")
	redo := fs.String("rederive", "", "rewrite this variants file's expectations with this build's derivation (texts untouched), then stop")
	conc := fs.Int("concurrency", 3, "generator calls at once")
	if err := fs.Parse(args); err != nil {
		return err
	}
	seeds, err := loadSeeds(*seedsGlob, *only)
	if err != nil {
		return err
	}
	if *redo != "" {
		return rederiveFile(seeds, *redo)
	}
	if *out == "" {
		return errors.New("call-gen: --out FILE")
	}
	base := *url
	if base == "" && !*offline {
		env, _, _, err := sandboxEnv(*sandbox)
		if err != nil {
			return err
		}
		base = env["SBX_LLM_TAP"]
	}
	picks := sampleDims(seeds, *count, *rng)
	scs := make([]CallScenario, len(picks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(1, *conc))
	for i, dims := range picks {
		seed := seeds[i%len(seeds)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var w writer
			if !*offline {
				w = func(msgs []map[string]string, n int) (string, error) {
					return chatComplete(ctx, base, *model, msgs, 2000, 0.8, *rng*1000+uint64(i)*10+uint64(n), map[string]any{"reasoning_budget": 0})
				}
			}
			scs[i] = makeVariant(seed, dims, *rng, i, w)
			fmt.Fprintf(stdout, "  %s  %s  (model wrote: %s)\n", scs[i].Name, dimsLine(dims), orDash(strings.Join(scs[i].Variant.Generated, ",")))
		}()
	}
	wg.Wait()
	b, _ := json.MarshalIndent(scs, "", "  ")
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %d variants to %s (pair coverage %.0f%%)\n", len(scs), *out, 100*pairCoverage(seeds, picks))
	return ctx.Err()
}

// rederive is a variant's expectations from this build's derive (nil when it isn't one, or
// its seed is gone): expectations follow the code, the texts stay what the model wrote.
func rederive(seeds []CallSeed, v *Variant, saved []map[string]string) *CallExpect {
	if v == nil {
		return nil
	}
	for _, s := range seeds {
		if s.Name == v.Seed {
			e := derive(s, v.Dims, saved, v.Offers)
			return &e
		}
	}
	return nil
}

// rederiveFile rewrites a variants file's expectations in place.
func rederiveFile(seeds []CallSeed, path string) error {
	scs, err := loadCallScenarios([]string{path}, "")
	if err != nil {
		return err
	}
	for i := range scs {
		if e := rederive(seeds, scs[i].Variant, scs[i].CallContext); e != nil {
			scs[i].Expect = *e
		}
	}
	b, _ := json.MarshalIndent(scs, "", "  ")
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func loadSeeds(glob, only string) ([]CallSeed, error) {
	paths, err := filepath.Glob(glob)
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("call-gen: no seed files match %q", glob)
	}
	var seeds []CallSeed
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var many []CallSeed
		if err := json.Unmarshal(b, &many); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		for _, s := range many {
			if only == "" || slices.Contains(strings.Split(only, ","), s.Name) {
				seeds = append(seeds, s)
			}
		}
	}
	if len(seeds) == 0 {
		return nil, errors.New("call-gen: no seeds")
	}
	return seeds, nil
}

func dimsLine(dims map[string]string) string {
	var b []string
	for _, d := range dimensions {
		if v, ok := dims[d.Name]; ok {
			b = append(b, d.Name+"="+v)
		}
	}
	return strings.Join(b, " ")
}

// writer asks the generator model; attempt n (0, 1) varies its seed.
type writer func(msgs []map[string]string, n int) (string, error)

const genSystem = `You write realistic test scenarios for an AI phone assistant that places calls to businesses for a household. Output ONLY one JSON object, no markdown.`

// generated is what the model writes for a variant.
type generated struct {
	Request string            `json:"request"`
	Persona string            `json:"persona"`
	Answers []string          `json:"answers"`
	Saved   map[string]string `json:"saved"`
}

// makeVariant is one variant: templates first, then each text the model wrote that keeps the
// dimension's meaning. Expectations are derived from the dimensions and the final details.
func makeVariant(seed CallSeed, dims map[string]string, rng uint64, i int, w writer) CallScenario {
	rules, offers := seed.calleeRules(dims)
	v := &Variant{Seed: seed.Name, RNG: rng, Index: i, Dims: dims, Offers: offers}
	request := seed.templateRequest(dims)
	persona := seed.Callee
	answers := seed.templateAnswers(dims["caller"])
	saved := seed.savedFor(dims["saved"])
	if w != nil {
		prompt := seed.genPrompt(dims, rules, saved)
		has := func(f string) bool { return slices.Contains(v.Generated, f) }
		// Each text is taken from the first attempt that keeps its dimension; a second
		// attempt only fills what the first got wrong.
		for n := range 2 {
			txt, err := w([]map[string]string{{"role": "system", "content": genSystem}, {"role": "user", "content": prompt}}, n)
			var g generated
			if err != nil || json.Unmarshal([]byte(jsonObject(txt)), &g) != nil {
				continue
			}
			if !has("request") && seed.validRequest(dims, g.Request) {
				request, v.Generated = strings.TrimSpace(g.Request), append(v.Generated, "request")
			}
			if p := strings.TrimSpace(g.Persona); !has("callee") && len(p) > 40 && strings.Contains(strings.ToLower(p), strings.ToLower(firstWord(seed.Callee))) {
				persona, v.Generated = p, append(v.Generated, "callee")
			}
			if !has("answers") && dims["caller"] != "never" && validAnswers(dims["caller"], g.Answers) {
				answers, v.Generated = g.Answers, append(v.Generated, "answers")
			}
			if !has("saved") && validSaved(saved, g.Saved) {
				for _, f := range saved {
					if nv := g.Saved[f["key"]]; nv != "" && f["key"] != "full_name" {
						f["value"] = strings.TrimSpace(nv)
					}
				}
				v.Generated = append(v.Generated, "saved")
			}
			if has("request") {
				break
			}
		}
	}
	slices.Sort(v.Generated)
	sc := CallScenario{
		Name:        fmt.Sprintf("%s/v%02d", seed.Name, i),
		Request:     request,
		Callee:      persona + "\n\nThe rules of this call (follow them exactly):\n- " + strings.Join(rules, "\n- "),
		Contact:     seed.Contact,
		CallContext: saved,
		NodeContext: seed.NodeContext,
		Answers:     answers,
		Expect:      derive(seed, dims, saved, offers),
		Variant:     v,
	}
	if dims["caller"] == "late" {
		sc.AnswerAfter = answerDelay
	}
	if len(sc.CallContext) == 0 {
		sc.CallContext = nil
	}
	return sc
}

// firstWord is the persona's name ("You are Gina, …" → "Gina"), which a rewrite must keep.
func firstWord(persona string) string {
	f := strings.Fields(strings.TrimPrefix(persona, "You are "))
	if len(f) == 0 {
		return ""
	}
	return strings.Trim(f[0], ",.")
}

func jsonObject(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return ""
	}
	return s[i : j+1]
}

func (s CallSeed) templateRequest(dims map[string]string) string {
	party, name := "", ""
	if s.Party > 0 && dims["brief"] != "no-party" {
		party = " for " + strconv.Itoa(s.Party)
	}
	if dims["brief"] != "no-name" {
		name = ", under the name " + variantName
	}
	r := strings.NewReplacer("{when}", s.when(dims), "{party}", party, "{name}", name).Replace(s.Request)
	return r
}

var partyWordRE = regexp.MustCompile(`(?i)\bfor (\d{1,2}|one|two|three|four|five|six)\b([^:.\d]|$)|\b(people|guests|of us|party of)\b`)

// validRequest: a generated request keeps the business, the phrasing, the party size and the
// name exactly as the dimensions say.
func (s CallSeed) validRequest(dims map[string]string, r string) bool {
	l := strings.ToLower(r)
	if len(r) < 15 || strings.Contains(r, "{") || !strings.Contains(l, strings.ToLower(firstWordOf(s.Business))) {
		return false
	}
	if m := s.whenMarker(dims["phrasing"]); m != "" && !strings.Contains(l, m) {
		return false
	}
	if p := dims["phrasing"]; s.Timed && s.OfferDay == "" && p != "after-work" {
		if !strings.Contains(l, strconv.Itoa(s.target(dims).h12())) {
			return false
		}
		ownDay := func(d string) bool { return strings.Contains(l, d) && !strings.Contains(strings.ToLower(s.Request), d) } // "my Monday appointment"
		if p == "no-day" && slices.ContainsFunc(weekdays, ownDay) ||
			p == "no-day" && regexp.MustCompile(`tomorrow|tonight|today|next week`).MatchString(l) {
			return false
		}
	}
	if s.Party > 0 {
		has := partyWordRE.MatchString(r)
		if dims["brief"] == "no-party" && has || dims["brief"] != "no-party" && !strings.Contains(l, strconv.Itoa(s.Party)) && !strings.Contains(l, numberWords[s.Party]) {
			return false
		}
	}
	hasName := strings.Contains(l, "jordan")
	return hasName == (dims["brief"] != "no-name")
}

func firstWordOf(s string) string {
	for _, w := range strings.Fields(s) {
		if len(w) >= 4 {
			return strings.Trim(w, "'’s")
		}
	}
	return s
}

// validSaved: the model gave every non-name detail a plausible value of the same shape.
func validSaved(saved []map[string]string, got map[string]string) bool {
	if len(saved) == 0 || got == nil {
		return false
	}
	for _, f := range saved {
		if f["key"] == "full_name" {
			continue
		}
		v := strings.TrimSpace(got[f["key"]])
		if v == "" || len(v) > 60 || (len(digitRuns(f["value"], 1)) > 0) != (len(digitRuns(v, 1)) > 0) {
			return false
		}
	}
	return true
}

// genPrompt asks for the variant's texts. The rules are shown so the texts fit them; they are
// appended to the persona verbatim either way.
func (s CallSeed) genPrompt(dims map[string]string, rules []string, saved []map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Business: %s. The call is about %s.\n\n", s.Business, s.Task)
	fmt.Fprintf(&b, "1. \"request\": what the household member (%s) says to their voice assistant to start this call: one natural spoken sentence, casual or terse like a real person. It must:\n", variantName)
	fmt.Fprintf(&b, "   - name the business (%s)\n", s.Business)
	if w := strings.TrimSpace(s.when(dims)); w != "" {
		fmt.Fprintf(&b, "   - say when exactly like this: %q (keep those words)\n", w)
	}
	if dims["phrasing"] == "no-day" {
		b.WriteString("   - name NO day or date at all\n")
	}
	if s.Party > 0 {
		if dims["brief"] == "no-party" {
			b.WriteString("   - NOT say how many people\n")
		} else {
			fmt.Fprintf(&b, "   - say it's for %d people\n", s.Party)
		}
	}
	if dims["brief"] == "no-name" {
		b.WriteString("   - NOT give any name\n")
	} else {
		fmt.Fprintf(&b, "   - give the name %s\n", variantName)
	}
	fmt.Fprintf(&b, "   Example: %q\n\n", s.templateRequest(dims))
	fmt.Fprintf(&b, "2. \"persona\": rewrite this description of who answers the phone, in second person, 2-4 sentences, giving them a distinct personality and a realistic way of talking. Keep their name, their greeting and what they do; add no prices, policies or demands of your own:\n   %q\n", s.Callee)
	fmt.Fprintf(&b, "   These rules will be added after it, so stay consistent with them:\n   - %s\n\n", strings.Join(rules, "\n   - "))
	switch dims["caller"] {
	case "never":
		b.WriteString("3. \"answers\": [] (the household member never answers the assistant's questions).\n\n")
	default:
		want := map[string]string{"immediate": "a clear yes / go ahead to what the assistant asks", "late": "a clear yes / go ahead to what the assistant asks",
			"vague": "a vague non-answer such as \"whatever works\" that neither agrees nor refuses", "contradicts": "a change of mind that contradicts the original request: don't do it / never mind"}[dims["caller"]]
		fmt.Fprintf(&b, "3. \"answers\": 1-2 short texts the household member types on their phone when the assistant asks them a question mid-call: %s. Example: %q\n\n", want, s.templateAnswers(dims["caller"]))
	}
	if len(saved) > 1 {
		b.WriteString("4. \"saved\": realistic but fake values for these saved personal details, same shape as the examples, keyed by key:\n")
		for _, f := range saved {
			if f["key"] != "full_name" {
				fmt.Fprintf(&b, "   - %s (example %q)\n", f["key"], f["value"])
			}
		}
	} else {
		b.WriteString("4. \"saved\": {}\n")
	}
	b.WriteString("\nReply with {\"request\": ..., \"persona\": ..., \"answers\": [...], \"saved\": {...}}.")
	return b.String()
}

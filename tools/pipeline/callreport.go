package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// pipeline call-report: pass rates per dimension value and per seed, the checks that fail,
// the judge's scores, turn latency and the worst calls, from the records pipeline call --out
// wrote. --baseline compares with an earlier report's JSON and flags regressions.

// Rate is a pass count.
type Rate struct {
	N    int `json:"n"`
	Pass int `json:"pass"`
}

func (r Rate) pct() float64 { return float64(r.Pass) / math.Max(1, float64(r.N)) }

// CallReport is call-report's JSON.
type CallReport struct {
	Calls         int                        `json:"calls"`
	HarnessErrors int                        `json:"harness_errors"`
	Overall       Rate                       `json:"overall"`
	Dims          map[string]map[string]Rate `json:"dims"`
	Seeds         map[string]Rate            `json:"seeds"`
	Checks        map[string]Rate            `json:"checks"`
	Judge         struct {
		N          int     `json:"n"`
		Overall    float64 `json:"overall"`
		RoleFlips  int     `json:"role_flip_calls"`
		Naturalnes float64 `json:"naturalness"`
		Honesty    float64 `json:"honesty"`
		Efficiency float64 `json:"efficiency"`
		Goal       float64 `json:"goal"`
	} `json:"judge"`
	TurnMS struct {
		N   int     `json:"n"`
		P50 float64 `json:"p50"`
		P95 float64 `json:"p95"`
		Max float64 `json:"max"`
	} `json:"turn_ms"`
	Worst       []worstCall `json:"worst"`
	Regressions []string    `json:"regressions,omitempty"`
	Ignored     string      `json:"ignored,omitempty"` // checks left out of pass/fail
}

type worstCall struct {
	Name    string   `json:"name"`
	File    string   `json:"file"`
	Fails   []string `json:"fails"`
	Judge   int      `json:"judge,omitempty"`
	Excerpt []string `json:"excerpt,omitempty"`
}

func runCallReport(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("call-report", flag.ContinueOnError)
	md := fs.String("md", "", "write the Markdown report here (default stdout)")
	js := fs.String("json", "", "write the JSON report here")
	baseline := fs.String("baseline", "", "an earlier report's JSON: flag dimension values and seeds whose pass rate dropped")
	drop := fs.Float64("drop", 0.15, "a pass-rate drop at least this large (with 3+ calls on both sides) is a regression")
	worst := fs.Int("worst", 10, "how many of the worst calls to list")
	ignore := fs.String("ignore", "", "checks left out of pass/fail (comma-separated), e.g. silence when the model server is shared and its latency is not the product's")
	recheck := fs.Bool("recheck", false, "re-derive variants' expectations and re-run the deterministic checks with this build (no calls)")
	seedsGlob := fs.String("seeds", "tools/pipeline/calls/seeds/*.json", "with --recheck: the seeds variants are re-derived from")
	maxTurn := fs.Duration("max-turn", 8*time.Second, "with --recheck: a turn slower than this is a long silence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("call-report: name record directories (pipeline call --out DIR)")
	}
	recs, err := loadRecords(fs.Args())
	if err != nil {
		return err
	}
	if *recheck {
		seeds, _ := loadSeeds(*seedsGlob, "") // no seeds: hand-written expectations only
		for _, r := range recs {
			if e := rederive(seeds, r.Variant, r.CallContext); e != nil {
				r.Expect = *e
			}
			if len(r.Checks) > 0 { // a call that failed before its checks has none to redo
				r.Checks = runChecks(r.Expect, r.facts(), *maxTurn)
			}
		}
	}
	if *ignore != "" {
		skip := strings.Split(*ignore, ",")
		for _, r := range recs {
			r.Checks = slices.DeleteFunc(r.Checks, func(c CheckResult) bool { return slices.Contains(skip, c.Name) })
		}
	}
	rep := buildReport(recs, *worst)
	rep.Ignored = *ignore
	if *baseline != "" {
		b, err := os.ReadFile(*baseline)
		if err != nil {
			return err
		}
		var base CallReport
		if err := json.Unmarshal(b, &base); err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
		rep.Regressions = regressions(base, rep, *drop)
	}
	text := rep.markdown()
	if *md != "" {
		if err := os.WriteFile(*md, []byte(text), 0o644); err != nil {
			return err
		}
	} else {
		fmt.Fprint(stdout, text)
	}
	if *js != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*js, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	if len(rep.Regressions) > 0 {
		return errFailed{len(rep.Regressions)}
	}
	return nil
}

func loadRecords(dirs []string) ([]*CallRecord, error) {
	var out []*CallRecord
	for _, d := range dirs {
		paths, _ := filepath.Glob(filepath.Join(d, "*.json"))
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			var r CallRecord
			if err := json.Unmarshal(b, &r); err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			r.file = p
			if r.Started.IsZero() { // a record from before these fields: the call ended if it has a result card
				if st, err := os.Stat(p); err == nil {
					r.Started = st.ModTime()
				}
				r.Ended = r.Card.Title != ""
			}
			out = append(out, &r)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("call-report: no records")
	}
	return out, nil
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}

// buildReport scores the records. A call that only hit harness errors is left out of the pass
// rates (it says nothing about the product) and counted apart.
func buildReport(recs []*CallRecord, nWorst int) CallReport {
	rep := CallReport{Dims: map[string]map[string]Rate{}, Seeds: map[string]Rate{}, Checks: map[string]Rate{}}
	inc := func(r Rate, pass bool) Rate {
		r.N++
		if pass {
			r.Pass++
		}
		return r
	}
	var turns []float64
	var js []*Judgement
	for _, r := range recs {
		rep.Calls++
		if r.HarnessError != "" && !r.passed() {
			rep.HarnessErrors++
			continue
		}
		pass := r.passed()
		rep.Overall = inc(rep.Overall, pass)
		seed := r.Name
		if r.Variant != nil {
			seed = r.Variant.Seed
			for d, v := range r.Variant.Dims {
				if rep.Dims[d] == nil {
					rep.Dims[d] = map[string]Rate{}
				}
				rep.Dims[d][v] = inc(rep.Dims[d][v], pass)
			}
		}
		rep.Seeds[seed] = inc(rep.Seeds[seed], pass)
		if len(r.Fails) > 0 {
			rep.Checks["call"] = inc(rep.Checks["call"], false)
		}
		for _, c := range r.Checks {
			rep.Checks[c.Name] = inc(rep.Checks[c.Name], c.Pass)
		}
		for _, t := range r.Transcript {
			turns = append(turns, t.Timings.TotalMS)
		}
		if r.Judge != nil && r.Judge.Error == "" {
			js = append(js, r.Judge)
		}
	}
	rep.TurnMS.N, rep.TurnMS.P50, rep.TurnMS.P95, rep.TurnMS.Max = len(turns), percentile(turns, .5), percentile(turns, .95), percentile(turns, 1)
	rep.Judge.N = len(js)
	for _, j := range js {
		n := float64(len(js))
		rep.Judge.Overall += float64(j.Overall) / n
		rep.Judge.Naturalnes += float64(j.Naturalness) / n
		rep.Judge.Honesty += float64(j.Honesty) / n
		rep.Judge.Efficiency += float64(j.Efficiency) / n
		rep.Judge.Goal += float64(j.Goal) / n
		if !j.RoleConsistent {
			rep.Judge.RoleFlips++
		}
	}
	// Worst: most failed checks, then the lowest judge score.
	bad := slices.Clone(recs)
	score := func(r *CallRecord) (int, int) {
		j := 6
		if r.Judge != nil && r.Judge.Error == "" {
			j = r.Judge.Overall
			if !r.Judge.RoleConsistent {
				j--
			}
		}
		return len(r.failures()), j
	}
	sort.SliceStable(bad, func(a, b int) bool {
		fa, ja := score(bad[a])
		fb, jb := score(bad[b])
		if fa != fb {
			return fa > fb
		}
		return ja < jb
	})
	for _, r := range bad {
		if len(rep.Worst) == nWorst {
			break
		}
		f, j := score(r)
		if f == 0 && j >= 4 {
			break
		}
		w := worstCall{Name: r.Name, File: r.file, Fails: r.failures()}
		if r.Judge != nil && r.Judge.Error == "" {
			w.Judge = r.Judge.Overall
			w.Fails = append(w.Fails, prefixed("judge: ", r.Judge.Problems)...)
			w.Fails = append(w.Fails, prefixed("role flip: ", r.Judge.RoleFlips)...)
		}
		for _, l := range r.Lines {
			if l.Who == "jarvis" || l.Who == "host" || l.Who == "caller" {
				w.Excerpt = append(w.Excerpt, fmt.Sprintf("%s: %s", l.Who, clip(l.Text, 160)))
			}
		}
		rep.Worst = append(rep.Worst, w)
	}
	return rep
}

func prefixed(p string, xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = p + x
	}
	return out
}

// regressions are the dimension values, seeds and checks whose pass rate fell by drop or more.
func regressions(base, now CallReport, drop float64) []string {
	var out []string
	cmp := func(what string, b, n Rate) {
		if b.N >= 3 && n.N >= 3 && b.pct()-n.pct() >= drop {
			out = append(out, fmt.Sprintf("%s: %.0f%% → %.0f%% (%d/%d → %d/%d)", what, 100*b.pct(), 100*n.pct(), b.Pass, b.N, n.Pass, n.N))
		}
	}
	cmp("overall", base.Overall, now.Overall)
	for d, vs := range now.Dims {
		for v, r := range vs {
			cmp(d+"="+v, base.Dims[d][v], r)
		}
	}
	for s, r := range now.Seeds {
		cmp("seed "+s, base.Seeds[s], r)
	}
	for c, r := range now.Checks {
		cmp("check "+c, base.Checks[c], r)
	}
	slices.Sort(out)
	return out
}

func (rep CallReport) markdown() string {
	var b strings.Builder
	rate := func(r Rate) string { return fmt.Sprintf("%d/%d (%.0f%%)", r.Pass, r.N, 100*r.pct()) }
	fmt.Fprintf(&b, "# Call variants\n\n%d calls: **%s pass**", rep.Calls, rate(rep.Overall))
	if rep.HarnessErrors > 0 {
		fmt.Fprintf(&b, ", %d left out for harness errors", rep.HarnessErrors)
	}
	if rep.Ignored != "" {
		fmt.Fprintf(&b, " (checks not counted: %s)", rep.Ignored)
	}
	fmt.Fprintf(&b, ".\n\nTurn latency (transcript total_ms, %d turns): p50 %.1fs, p95 %.1fs, max %.1fs.\n\n",
		rep.TurnMS.N, rep.TurnMS.P50/1000, rep.TurnMS.P95/1000, rep.TurnMS.Max/1000)
	if rep.Judge.N > 0 {
		fmt.Fprintf(&b, "Judge (%d calls, 1-5): overall %.1f, naturalness %.1f, honesty %.1f, efficiency %.1f, goal %.1f; role flips on %d calls.\n\n",
			rep.Judge.N, rep.Judge.Overall, rep.Judge.Naturalnes, rep.Judge.Honesty, rep.Judge.Efficiency, rep.Judge.Goal, rep.Judge.RoleFlips)
	}
	if len(rep.Regressions) > 0 {
		b.WriteString("## ⚠️ Regressions against the baseline\n\n")
		for _, r := range rep.Regressions {
			fmt.Fprintf(&b, "- %s\n", r)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Checks\n\n| check | pass |\n| --- | --- |\n")
	for _, c := range sortedKeys(rep.Checks) {
		fmt.Fprintf(&b, "| %s | %s |\n", c, rate(rep.Checks[c]))
	}
	b.WriteString("\n## By dimension\n\n")
	for _, d := range dimensions {
		vs := rep.Dims[d.Name]
		if len(vs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "**%s**: ", d.Name)
		var parts []string
		for _, v := range d.Values {
			if r, ok := vs[v]; ok {
				parts = append(parts, fmt.Sprintf("%s %s", v, rate(r)))
			}
		}
		b.WriteString(strings.Join(parts, " · ") + "\n\n")
	}
	b.WriteString("## By seed\n\n| seed | pass |\n| --- | --- |\n")
	for _, s := range sortedKeys(rep.Seeds) {
		fmt.Fprintf(&b, "| %s | %s |\n", s, rate(rep.Seeds[s]))
	}
	if len(rep.Worst) > 0 {
		b.WriteString("\n## Worst calls\n\n")
		for _, w := range rep.Worst {
			fmt.Fprintf(&b, "### %s\n\n[%s](%s)", w.Name, filepath.Base(w.File), w.File)
			if w.Judge > 0 {
				fmt.Fprintf(&b, " · judge %d/5", w.Judge)
			}
			b.WriteString("\n\n")
			for _, f := range w.Fails {
				fmt.Fprintf(&b, "- %s\n", f)
			}
			if len(w.Excerpt) > 0 {
				b.WriteString("\n```\n" + strings.Join(w.Excerpt[:min(len(w.Excerpt), 14)], "\n") + "\n```\n")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

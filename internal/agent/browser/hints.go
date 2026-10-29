package browser

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

var (
	// "type dogs into the search box", "type dogs into Search".
	reTypeInto = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?(?:type|enter|put)\s+(.+?)\s+(?:in|into|in\s+to|on)\s+(?:box\s+|field\s+)?(.+?)[\s.!?]*$`)
	// Common role words are redundant with the accessible role: "the search box" is "Search".
	reLeadingDeterminer = regexp.MustCompile(`(?i)^(?:the|a|my)\s+`)
	reRoleSuffix        = regexp.MustCompile(`(?i)\s+(?:box|field|bar|button|link|tab)$`)
	// "Click" or a common whisper substitution in the final sentence, even after earlier speech.
	reClickHeard = regexp.MustCompile(`(?i)(?:^|[.!?,]\s+)(quick|good|clique|klick|clip|click)\s+(?:on\s+)?([^.!?,]+?)[\s.!?]*$`)
	reClickVerb  = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?(?:click|tap)\s`) // not "press": "press enter" is a key
	reHintVerb   = regexp.MustCompile(`(?i)^\s*(?:click|press|open|tap|select|choose|pick|go\s+to)\s+(?:on\s+)?(.+?)[\s.!?]*$`)
	reOrdinal    = regexp.MustCompile(`(?i)^\s*(?:the\s+)?(first|1st|second|2nd|third|3rd|fourth|4th|fifth|5th|sixth|6th|seventh|7th|eighth|8th|ninth|9th|tenth|10th)\s+(video|link|result|search\s+result)(?:\s+(?:on\s+the\s+page|in\s+the\s+results))?[\s.!?]*$`)
)

var ordinalNumbers = map[string]int{
	"first": 1, "1st": 1, "second": 2, "2nd": 2, "third": 3, "3rd": 3, "fourth": 4, "4th": 4,
	"fifth": 5, "5th": 5, "sixth": 6, "6th": 6, "seventh": 7, "7th": 7, "eighth": 8, "8th": 8,
	"ninth": 9, "9th": 9, "tenth": 10, "10th": 10,
}

var numberWords = map[string]int{
	"zero": 0, "one": 1, "two": 2, "to": 2, "too": 2, "three": 3, "four": 4, "for": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19,
	"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
	"hundred": 100, "a hundred": 100, "one hundred": 100,
}

// spokenNumber is used to parse spoken media durations ("skip ahead thirty seconds"); page
// element selection itself no longer exposes numbered targets.
func spokenNumber(s string) (int, bool) {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), ".!?,"))
	if n, err := strconv.Atoi(s); err == nil {
		return n, n > 0
	}
	if n, ok := numberWords[s]; ok {
		return n, n > 0
	}
	total, seen := 0, false
	for _, w := range strings.Fields(strings.NewReplacer("-", " ", " and ", " ").Replace(s)) {
		n, ok := numberWords[w]
		if !ok {
			return 0, false
		}
		if n == 100 {
			total = max(total, 1) * 100
		} else {
			total += n
		}
		seen = true
	}
	return total, seen && total > 0
}

// hintState is the current browser snapshot used to resolve spoken control descriptions.
type hintState struct {
	tab       any
	gen       float64
	url       string
	names     map[int]string
	roles     map[int]string
	contexts  map[int]string
	positions map[int]string
	targets   map[int]string
	refs      map[string]int
	// Interactive elements in page order. Refs are internal and are never displayed.
	candidates []targetCandidate
}

type targetCandidate struct {
	key string
	ref int
}

const (
	nearScore  = 0.8 // winmatch.Similarity a misheard name needs ("adil" for "Adele")
	nearMargin = 0.1 // ahead of the next different name by this much
)

// ref resolves a unique exact name or a safe fuzzy spoken-name match. Context descriptions and
// repeated names are left to the confidence-gated picker.
func (h *hintState) ref(said string, loose bool) (ref int, ok bool) {
	said = strings.TrimSpace(reLeadingDeterminer.ReplaceAllString(strings.TrimSpace(said), ""))
	keys := []string{labelKey(said), labelKey(reRoleSuffix.ReplaceAllString(said, ""))}
	for _, k := range keys {
		if ref, ok = h.refs[k]; ok {
			if ref > 0 {
				return ref, true
			}
			var matches []int
			for _, c := range h.candidates {
				if c.key == k {
					matches = append(matches, c.ref)
				}
			}
			if ref, ok = h.equivalentRef(matches); ok {
				return ref, true
			}
		}
	}
	if !loose {
		return 0, false
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		var matches []int
		for _, b := range h.candidates {
			if b.key == k || strings.HasPrefix(b.key, k+" ") {
				matches = append(matches, b.ref)
			}
		}
		if ref, ok = h.equivalentRef(matches); ok {
			return ref, true
		}
	}
	// A misheard name: the best by sound or spelling, clearly ahead of any other name.
	best, second, bestRef := 0.0, 0.0, 0
	groupScores := make(map[string]struct {
		score float64
		ref   int
	})
	for _, b := range h.candidates {
		sc := winmatch.Similarity(keys[0], b.key)
		group := fmt.Sprintf("ref:%d", b.ref)
		if h.roles[b.ref] == "link" && h.targets[b.ref] != "" {
			group = "target:" + h.targets[b.ref]
		}
		if old, ok := groupScores[group]; !ok || sc > old.score {
			groupScores[group] = struct {
				score float64
				ref   int
			}{sc, b.ref}
		}
	}
	for _, candidate := range groupScores {
		sc := candidate.score
		switch {
		case sc > best:
			second = best
			best, bestRef = sc, candidate.ref
		case sc > second:
			second = sc
		}
	}
	if best >= nearScore && best-second >= nearMargin {
		return bestRef, true
	}
	return 0, false
}

// Equivalent link refs lead to the same destination, as with a result thumbnail and its title.
// Selecting either is safe and avoids a model round trip for the common duplicate-link pattern.
func (h *hintState) equivalentRef(refs []int) (int, bool) {
	if len(refs) == 0 {
		return 0, false
	}
	first := refs[0]
	if len(refs) == 1 {
		return first, true
	}
	target := h.targets[first]
	if target == "" || h.roles[first] != "link" {
		return 0, false
	}
	for _, ref := range refs[1:] {
		if h.roles[ref] != "link" || h.targets[ref] != target {
			return 0, false
		}
	}
	return first, true
}

// ordinalRef resolves a visible result whose video destination is explicit in its link URL.
// Duplicate thumbnail/title anchors to the same video count as one result.
func (h *hintState) ordinalRef(said string) (int, bool) {
	m := reOrdinal.FindStringSubmatch(said)
	if m == nil {
		return 0, false
	}
	want, ok := ordinalNumbers[strings.ToLower(m[1])]
	if !ok {
		return 0, false
	}
	kind := strings.ToLower(strings.Join(strings.Fields(m[2]), " "))
	if kind != "video" && !(isVideoResultsPage(h.url) && (kind == "link" || kind == "result" || kind == "search result")) {
		return 0, false
	}
	seen := make(map[string]bool)
	position := 0
	for _, candidate := range h.candidates {
		if h.roles[candidate.ref] != "link" {
			continue
		}
		key := videoTargetKey(h.targets[candidate.ref])
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		position++
		if position == want {
			return candidate.ref, true
		}
	}
	return 0, false
}

func isVideoResultsPage(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if isDomain(host, "youtube.com") && u.Path == "/results" {
		return true
	}
	if isDomain(host, "google.com") && u.Path == "/search" {
		q := u.Query()
		return q.Get("tbm") == "vid" || q.Get("udm") == "7"
	}
	return isDomain(host, "bing.com") && u.Path == "/videos/search"
}

// videoTargetKey returns a stable identity for common video result URLs, unwrapping Google
// result redirects where needed. Other links are not guessed to be videos.
func videoTargetKey(raw string) string {
	return videoTargetKeyAt(raw, 0)
}

func videoTargetKeyAt(raw string, depth int) string {
	if depth > 4 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if isDomain(host, "google.com") && (u.Path == "/url" || u.Path == "/url/") {
		for _, q := range []string{u.Query().Get("q"), u.Query().Get("url")} {
			if key := videoTargetKeyAt(q, depth+1); key != "" {
				return key
			}
		}
		return ""
	}
	parts := strings.FieldsFunc(strings.Trim(u.Path, "/"), func(r rune) bool { return r == '/' })
	switch {
	case host == "youtu.be":
		if len(parts) > 0 {
			return "youtube:" + parts[0]
		}
	case isDomain(host, "youtube.com"):
		if u.Path == "/watch" && u.Query().Get("v") != "" {
			return "youtube:" + u.Query().Get("v")
		}
		if len(parts) > 1 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed") {
			return "youtube:" + parts[1]
		}
	case isDomain(host, "vimeo.com"):
		for i, part := range parts {
			if part == "video" && i+1 < len(parts) {
				return "vimeo:" + parts[i+1]
			}
		}
		if len(parts) > 0 && isDigits(parts[0]) {
			return "vimeo:" + parts[0]
		}
	case host == "dai.ly":
		if len(parts) > 0 {
			return "dailymotion:" + parts[0]
		}
	case isDomain(host, "dailymotion.com"):
		for i, part := range parts {
			if part == "video" && i+1 < len(parts) {
				return "dailymotion:" + strings.SplitN(parts[i+1], "_", 2)[0]
			}
		}
	}
	return ""
}

func isDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// labelKey is a label or what was said for one, compared: lower case, words only, number words
// as digits ("Reply two" is "reply 2").
func labelKey(s string) string {
	w := strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			return unicode.ToLower(r)
		}
		return ' '
	}, s))
	for i, x := range w {
		if n, ok := numberWords[x]; ok && n > 0 && x != "to" && x != "too" && x != "for" {
			w[i] = strconv.Itoa(n)
		}
	}
	return strings.Join(w, " ")
}

// directHints resolves direct browser actions against the current page snapshot.
func (t *Tools) directHints(ctx context.Context, said string) (map[string]any, bool) {
	t.mu.Lock()
	h := t.hints
	t.mu.Unlock()
	if h == nil {
		// No snapshot, or a step changed the page since (refreshHints): read it now, once
		// it has loaded, while a turn listens or for a "click …" with the browser in front.
		t.mu.Lock()
		on := t.listening
		t.mu.Unlock()
		heardClick := reClickHeard.FindStringSubmatch(said)
		click := reClickVerb.MatchString(said) || (heardClick != nil && strings.EqualFold(heardClick[1], "click"))
		if !(on || click) || !node.Enabled(ctx, ToolClick) || !t.browserInFront() {
			return nil, false
		}
		t.readTargets(ctx, true)
		t.mu.Lock()
		h = t.hints
		t.mu.Unlock()
		if h == nil {
			if click {
				return noSuchTarget(strings.TrimSpace(said)), true
			}
			return nil, false
		}
	}
	if m := reTypeInto.FindStringSubmatch(said); m != nil {
		text := strings.TrimSpace(m[1])
		if q := reQuoted.FindStringSubmatch(text); q != nil {
			text = q[1]
		}
		ref, ok := h.ref(m[2], true)
		if ok {
			return t.fillHint(ctx, h, ref, text, false), true
		}
		if ref, result := t.pick(ctx, "field description: "+m[2], h); result == pickSelected {
			return t.fillHint(ctx, h, ref, text, false), true
		} else if result == pickUncertain {
			return uncertainTarget(), true
		}
		return nil, false // no plausible page field: dictation's ("type hello into Slack")
	}
	target := strings.TrimRight(strings.TrimSpace(said), ".!?")
	m := reHintVerb.FindStringSubmatch(said)
	if m != nil {
		target = m[1]
	}
	loose := m != nil
	if hm := reClickHeard.FindStringSubmatch(said); m == nil && hm != nil {
		target := strings.TrimSpace(hm[2])
		if r, found := h.ordinalRef(target); found {
			return t.clickHint(ctx, h, r), true
		}
		if r, found := h.ref(target, true); found {
			return t.clickHint(ctx, h, r), true
		}
		if r, result := t.pick(ctx, "click "+target, h); result == pickSelected {
			return t.clickHint(ctx, h, r), true
		} else if result == pickUncertain {
			return uncertainTarget(), true
		}
		if strings.EqualFold(hm[1], "click") {
			return noSuchTarget(target), true
		}
		// A literal click stays on the page handler on a miss; only a possible whisper substitution
		// falls through when nothing on the page fits, preserving speech such as "Good morning".
		return nil, false
	}
	if m != nil {
		if r, found := h.ordinalRef(target); found {
			return t.clickHint(ctx, h, r), true
		}
	}
	ref, ok := h.ref(target, loose)
	switch {
	case ok:
		return t.clickHint(ctx, h, ref), true
	case reClickVerb.MatchString(said):
		// "click …" with no name like it: the model picks from the page's list (pick.go), else
		// it stays here, never a free model turn (that made "click line over" a light).
		if ref, result := t.pick(ctx, said, h); result == pickSelected {
			return t.clickHint(ctx, h, ref), true
		} else if result == pickUncertain {
			return uncertainTarget(), true
		}
		return noSuchTarget(target), true
	}
	return nil, false
}

// namedRef resolves a spoken page name when a voice-turn snapshot is available.
func (t *Tools) namedRef(said string) (ref int, h *hintState, handled bool) {
	t.mu.Lock()
	h = t.hints
	t.mu.Unlock()
	if strings.TrimSpace(said) == "" {
		return 0, nil, false
	}
	if h == nil {
		return 0, nil, false
	}
	ref, ok := h.ref(said, true)
	return ref, h, ok
}

func noSuchTarget(said string) map[string]any {
	said = strings.TrimSpace(said)
	return map[string]any{"success": false, "error_code": "target_not_found", "error": fmt.Sprintf("no matching control for %s on the page", said),
		node.IslandKey: "I couldn't find that control. Please repeat the command with another detail.", node.IslandOnlyKey: true, node.ChoiceKey: true}
}

func uncertainTarget() map[string]any {
	return map[string]any{"success": false, "error_code": "target_uncertain", "error": "the intended control is ambiguous",
		node.IslandKey: "I'm not sure which control you mean. Please repeat that with another detail, like its nearby text or position.", node.IslandOnlyKey: true, node.ChoiceKey: true}
}

// reQuoted is text said in quotes, which keeps its own punctuation.
var reQuoted = regexp.MustCompile(`^["“'‘](.+?)["”'’][.!?,]?$`)

// readTargets takes an invisible snapshot of interactive controls, optionally after navigation.
func (t *Tools) readTargets(ctx context.Context, wait bool) map[string]any {
	if err := t.notJarvisPage(ctx, map[string]any{}, "Couldn't inspect page controls"); err != nil {
		return t.hintFail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	extra := map[string]any{"max": 150, "viewport": true}
	if wait {
		extra["wait"] = true
	}
	res, err := t.call(ctx, "snapshot", map[string]any{}, extra)
	if err != nil {
		return t.hintFail(err)
	}
	h := &hintState{gen: num(res["gen"]), url: str(res["url"]), names: map[int]string{}, roles: map[int]string{}, contexts: map[int]string{}, positions: map[int]string{}, targets: map[int]string{}, refs: map[string]int{}}
	if tab, ok := res["tab"].(map[string]any); ok {
		h.tab = tab["tab_id"]
	}
	els, _ := res["elements"].([]any)
	for _, e := range els {
		m, _ := e.(map[string]any)
		n, err := strconv.Atoi(strings.TrimPrefix(str(m["ref"]), "e"))
		if err != nil || n <= 0 {
			continue
		}
		name := str(m["name"])
		role := str(m["role"])
		context := str(m["context"])
		position := str(m["position"])
		target := str(m["target"])
		h.names[n], h.roles[n], h.contexts[n], h.positions[n], h.targets[n] = name, role, context, position, target
		key := labelKey(name)
		if key != "" {
			if _, exists := h.refs[key]; exists {
				h.refs[key] = 0 // repeated labels are grouped and resolved below
			} else {
				h.refs[key] = n
			}
		}
		h.candidates = append(h.candidates, targetCandidate{key: key, ref: n})
	}
	if len(h.names) == 0 {
		return map[string]any{"success": false, "error": "no interactive controls are visible on this page",
			node.IslandKey: "I don't see any controls on this page", node.IslandOnlyKey: true}
	}
	t.mu.Lock()
	t.hints, t.hintConfirm = h, ""
	t.mu.Unlock()
	// A list on the island: the next words are its answer, heard at once (the choice window).
	return map[string]any{"success": true, "count": len(h.names), node.IslandKey: "Say what to click",
		node.IslandOnlyKey: true, node.ChoiceKey: true}
}

// clickHint presses an internally referenced control; risky labels still ask first.
func (t *Tools) clickHint(ctx context.Context, h *hintState, n int) map[string]any {
	return t.actHint(ctx, h, n, "press", "", false)
}

// fillHint types text into an internally referenced control, through the existing safety gate.
func (t *Tools) fillHint(ctx context.Context, h *hintState, n int, text string, submit bool) map[string]any {
	clean, err := dictate.Sanitize(text)
	if err != nil {
		return t.hintFail(fmt.Errorf("%w: %w", errDictate, err))
	}
	return t.actHint(ctx, h, n, "set_value", clean, submit)
}

// actHint performs op on an internally referenced control.
func (t *Tools) actHint(ctx context.Context, h *hintState, n int, op, value string, submitting bool) map[string]any {
	if h == nil {
		return map[string]any{"success": false, "error_code": "no_page_snapshot",
			"error":        "there is no current browser page snapshot",
			node.IslandKey: "I can't identify that control yet", node.IslandOnlyKey: true}
	}
	name, ok := h.names[n]
	if !ok {
		return noSuchTarget("that control")
	}
	label := cmp.Or(name, h.contexts[n], h.roles[n], "unlabeled control")
	press := op == "press"
	what, outcome, question := "Clicking ", "Nothing was clicked", fmt.Sprintf("Click %q in the browser?", dictate.Preview(label, 60))
	if !press {
		what, outcome = "Typing into ", "Nothing was typed"
		question = fmt.Sprintf("Type %q into %q?", dictate.Preview(value, 60), dictate.Preview(label, 40))
		if submitting {
			question = fmt.Sprintf("Type %q into %q and submit?", dictate.Preview(value, 60), dictate.Preview(label, 40))
		}
	}
	args := map[string]any{"tab_id": h.tab}
	act := func(ctx context.Context) (map[string]any, error) {
		if err := t.notJarvisPage(ctx, args, outcome); err != nil {
			return nil, err
		}
		t.show(what + dictate.Preview(label, 60))
		extra := map[string]any{"gen": h.gen, "ref": fmt.Sprintf("e%d", n), "op": op}
		if !press {
			extra["value"] = value
			if submitting {
				extra["submit"] = true
			}
		}
		res, err := t.call(ctx, "act", args, extra)
		if press {
			t.clearTargetSnapshot(h) // a pressed control may have changed the page
		}
		if err != nil {
			return nil, err
		}
		if press || submitting {
			t.refreshHints(ctx)
			res["clicked"] = label
		} else {
			res["filled"] = label
		}
		res["success"] = true
		return res, nil
	}
	g := dictate.GateType
	// An unlabeled control asks too because its risk cannot be classified from its name.
	if press && t.Confirm.Needs(dictate.GateSubmit) && (strings.TrimSpace(name) == "" || clickRisk(name, h.url) != "") {
		g = dictate.GateSubmit
	} else if submitting && t.Confirm.Needs(dictate.GateSubmit) && submitRisk(name, h.url) != "" {
		g = dictate.GateSubmit
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	out, err := t.gate(ctx, g, question, act)
	if err != nil {
		return t.hintFail(err)
	}
	if out["status"] == "awaiting_confirmation" {
		q, _ := out["question"].(string)
		t.mu.Lock()
		t.hintConfirm, _ = out["confirmation_id"].(string)
		t.mu.Unlock()
		out[node.IslandKey], out[node.IslandOnlyKey], out[node.ChoiceKey] = dictate.WaitingPrefix+q, true, true
		return out
	}
	out[node.IslandOnlyKey] = true
	return out
}

// TurnListening snapshots visible controls while a voice turn listens.
func (t *Tools) TurnListening(ctx context.Context, on bool) {
	t.mu.Lock()
	t.listening = on
	t.mu.Unlock()
	t.clearTargets()
	if !on {
		return
	}
	if node.Enabled(ctx, ToolClick) && t.browserInFront() {
		t.readTargets(ctx, false)
	}
}

// refreshHints marks the snapshot stale after a step changed the page; the next command reads
// it again (directHints). Reading at once got the old page: a click's navigation hasn't begun
// when the click returns (2026-10-28).
func (t *Tools) refreshHints(context.Context) { t.clearTargets() }

func (t *Tools) browserInFront() bool {
	for _, b := range t.Hub.Browsers() {
		if b.Focused {
			return true
		}
	}
	return false
}

// clearTargets invalidates the active page snapshot.
func (t *Tools) clearTargets() {
	t.mu.Lock()
	t.hints = nil
	t.mu.Unlock()
}

// clearTargetSnapshot invalidates only the snapshot this action resolved against.
func (t *Tools) clearTargetSnapshot(h *hintState) {
	t.mu.Lock()
	if t.hints == h {
		t.hints = nil
	}
	t.mu.Unlock()
}

// AnswerChoice takes a target clarification or the yes/no answer to a risky action.
func (t *Tools) AnswerChoice(ctx context.Context, said string) (map[string]any, bool) {
	t.mu.Lock()
	id := t.hintConfirm
	t.hintConfirm = ""
	t.mu.Unlock()
	if id != "" && t.Confirm.Pending(id) {
		out, err := t.Confirm.Answer(ctx, id, said)
		if err != nil {
			return t.hintFail(err), true
		}
		out[node.IslandOnlyKey] = true
		return out, true
	}
	return t.directHints(ctx, said)
}

// hintToolOut is a page action's result for browser_fill or browser_click: no
// island markers (the model speaks for itself), and a failure as an error.
func hintToolOut(out map[string]any) (map[string]any, error) {
	delete(out, node.IslandKey)
	delete(out, node.IslandOnlyKey)
	delete(out, node.ChoiceKey)
	if out["success"] == false && out["status"] != "awaiting_confirmation" {
		return nil, &CommandError{Code: cmp.Or(str(out["error_code"]), "failed"), Message: str(out["error"])}
	}
	return out, nil
}

func (t *Tools) hintFail(err error) map[string]any {
	msg, shown := message(err), dictate.Preview(message(err), 80)
	if code(err) == "extension_outdated" {
		shown = "Browser controls need the newer Jarvis extension"
	}
	return map[string]any{"success": false, "error": msg, "error_code": code(err),
		node.IslandKey: shown, node.IslandOnlyKey: true}
}

func num(v any) float64 { f, _ := v.(float64); return f }
func str(v any) string  { s, _ := v.(string); return s }

var (
	_ node.ChoiceAnswerer = (*Tools)(nil)
	_ node.TurnWatcher    = (*Tools)(nil)
)

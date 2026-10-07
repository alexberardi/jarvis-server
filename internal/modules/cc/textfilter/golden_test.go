package textfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Golden tests against fixtures/golden/voice (tools/golden/export_cc_voice.py, the real Python).

func loadVoice(t *testing.T, name string) *pyjson.Object {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "voice", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v.(*pyjson.Object)
}

func rows(o *pyjson.Object, k string) []any { v, _ := o.Get(k); return v.([]any) }

func g(v any, k string) any {
	o, ok := v.(*pyjson.Object)
	if !ok {
		return nil
	}
	x, _ := o.Get(k)
	return x
}

func gs(v any, k string) string { s, _ := g(v, k).(string); return s }

func gb(v any, k string) bool { b, _ := g(v, k).(bool); return b }

func strs(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestShapesGolden(t *testing.T) {
	fx := loadVoice(t, "shapes.json")
	fns := map[string]func(string) bool{
		"is_stt_noise":                  IsSTTNoise,
		"is_device_command_shaped":      IsDeviceCommandShaped,
		"is_music_control_shaped":       IsMusicControlShaped,
		"has_multi_speaker_markers":     HasMultiSpeakerMarkers,
		"is_short_non_command_fragment": IsShortNonCommandFragment,
		"is_action_command_shaped":      IsActionCommandShaped,
		"is_report_shaped":              IsReportShaped,
		"is_question_shaped":            IsQuestionShaped,
		"response_claims_action":        ResponseClaimsAction,
	}
	n := 0
	for _, r := range rows(fx, "shapes") {
		text := gs(r, "text")
		for _, k := range r.(*pyjson.Object).Keys() {
			if k == "text" {
				continue
			}
			n++
			if got := fns[k](text); got != gb(r, k) {
				t.Errorf("%s(%q) = %v, want %v", k, text, got, !got)
			}
		}
	}
	for _, r := range rows(fx, "addressed") {
		n++
		got := AddressedHouseholdMember(gs(r, "text"), strs(g(r, "names")))
		if want := gs(r, "member"); got != want {
			t.Errorf("addressed(%q, %v) = %q, want %q", gs(r, "text"), g(r, "names"), got, want)
		}
	}
	t.Logf("%d shape checks", n)
}

func TestTextGolden(t *testing.T) {
	fx := loadVoice(t, "text.json")
	for _, r := range rows(fx, "clean_for_tts") {
		if got := CleanForTTS(gs(r, "text")); got != gs(r, "out") {
			t.Errorf("clean_for_tts(%q) = %q, want %q", gs(r, "text"), got, gs(r, "out"))
		}
	}
	for _, r := range rows(fx, "apply_exchange_complete") {
		got, eoe := ApplyExchangeComplete(gs(r, "stop_reason"), gs(r, "message"))
		if got != gs(r, "out") || eoe != gb(r, "end_of_exchange") {
			t.Errorf("apply(%q, %q) = %q,%v", gs(r, "stop_reason"), gs(r, "message"), got, eoe)
		}
	}
	for _, r := range rows(fx, "filler") {
		if got := RewriteTerminalFiller(gs(r, "stop_reason"), gs(r, "message")); got != gs(r, "out") {
			t.Errorf("filler(%v, %q) = %q", g(r, "stop_reason"), gs(r, "message"), got)
		}
	}
	for _, r := range rows(fx, "transient") {
		got := gs(r, "role") == "system" && IsTransientSystemBlockContent(gs(r, "content"))
		if got != gb(r, "transient") {
			t.Errorf("transient(%q) = %v", gs(r, "content"), got)
		}
	}
	for _, r := range rows(fx, "sentences") {
		text := gs(r, "text")
		if got := SplitSentenceBoundary(text); !eqStrs(got, strs(g(r, "split"))) {
			t.Errorf("split(%q) = %q", text, got)
		}
		if got := ExtractSentences(text); !eqStrs(got, strs(g(r, "sentences"))) {
			t.Errorf("sentences(%q) = %q", text, got)
		}
		if got := SpeechGroups(text); !eqStrs(got, strs(g(r, "groups"))) {
			t.Errorf("groups(%q) = %q", text, got)
		}
	}
	ts := NewThinkStripper("<think>", "</think>")
	for _, r := range rows(fx, "think") {
		text := gs(r, "text")
		if ts.StripCompleteBlocks(text) != gs(r, "strip_complete") || ts.HasOpenBlock(text) != gb(r, "has_open") ||
			ts.StripAll(text) != gs(r, "strip_all") {
			t.Errorf("think(%q)", text)
		}
	}
}

func TestWakeGolden(t *testing.T) {
	fx := loadVoice(t, "wake.json")
	for _, r := range rows(fx, "wake") {
		tr, ph := gs(r, "transcript"), gs(r, "phrase")
		if got := WakePhrasePresent(tr, ph); got != gb(r, "present") {
			t.Errorf("present(%q,%q) = %v", tr, ph, got)
		}
		want, _ := g(r, "similarity").(float64)
		if got := WakePhraseSimilarity(tr, ph); got != want {
			t.Errorf("similarity(%q,%q) = %v, want %v", tr, ph, got, want)
		}
	}
	for _, r := range rows(fx, "slice") {
		secs := g(r, "seconds").(float64)
		rate := int(pyInt(g(r, "rate")))
		chans := int(pyInt(g(r, "channels")))
		width := int(pyInt(g(r, "width")))
		src := testWAV(secs, rate, chans, width)
		if len(src) != int(pyInt(g(r, "in_len"))) {
			t.Fatalf("test WAV length %d, want %v", len(src), g(r, "in_len"))
		}
		out, ok := SliceLeadingWAV(src, WakeSliceSeconds)
		sum := sha256.Sum256(out)
		if !ok || len(out) != int(pyInt(g(r, "out_len"))) || hex.EncodeToString(sum[:]) != gs(r, "out_sha256") {
			t.Errorf("slice %v: len %d", r, len(out))
		}
	}
	if _, ok := SliceLeadingWAV([]byte("nope"), 1); ok {
		t.Error("garbage WAV must fail")
	}
}

func pyInt(v any) int64 {
	if b, ok := v.(interface{ Int64() int64 }); ok {
		return b.Int64()
	}
	return 0
}

// testWAV is the exporter's wav_bytes: frames of (i*7)%256 under Python's wave header.
func testWAV(seconds float64, rate, channels, width int) []byte {
	n := int(seconds * float64(rate))
	data := make([]byte, n*channels*width)
	for i := range data {
		data[i] = byte((i * 7) % 256)
	}
	w := []byte("RIFF")
	le32 := func(v int) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }
	le16 := func(v int) []byte { return []byte{byte(v), byte(v >> 8)} }
	w = append(w, le32(36+len(data))...)
	w = append(w, "WAVEfmt "...)
	w = append(w, le32(16)...)
	w = append(w, le16(1)...)
	w = append(w, le16(channels)...)
	w = append(w, le32(rate)...)
	w = append(w, le32(rate*channels*width)...)
	w = append(w, le16(channels*width)...)
	w = append(w, le16(width*8)...)
	w = append(w, "data"...)
	w = append(w, le32(len(data))...)
	return append(w, data...)
}

func TestAckGolden(t *testing.T) {
	fx := loadVoice(t, "ack.json")
	diverged := 0
	for _, r := range rows(fx, "ack") {
		text := gs(r, "text")
		got := AckPool(text)
		if !eqStrs(got, strs(g(r, "m5_pool"))) {
			t.Errorf("pool(%q) = %v, want the M5 pool %v", text, got, g(r, "m5_pool"))
		}
		legacy := strs(g(r, "legacy_pool"))
		if !eqStrs(legacy, strs(g(r, "m5_pool"))) {
			diverged++ // M5: legacy matched a keyword inside a longer word
		}
		for _, p := range strs(g(r, "legacy_picks")) {
			if !contains(legacy, p) {
				t.Errorf("legacy pick %q outside its pool", p)
			}
		}
		for i := range got {
			if a := Acknowledgment(text, func(n int) int { return i % n }); a != got[i] {
				t.Errorf("Acknowledgment pick %d = %q", i, a)
			}
		}
	}
	if diverged == 0 {
		t.Error("expected M5 word-boundary divergences in the corpus (e.g. train/rain, show/how)")
	}
	t.Logf("%d rows changed by the M5 word-boundary fix", diverged)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestEngineGolden(t *testing.T) {
	fx := loadVoice(t, "engine.json")
	for _, r := range rows(fx, "canonical") {
		key, ok := CanonicalArgsKey(gs(r, "arguments"))
		want, wantOK := g(r, "key").(string)
		if ok != wantOK || key != want {
			t.Errorf("canonical(%q) = %q,%v want %q", gs(r, "arguments"), key, ok, want)
		}
	}
	kw := rows(fx, "keywords")[0]
	var sources [][]any
	for _, s := range g(kw, "sources").([]any) {
		sources = append(sources, s.([]any))
	}
	if got := CollectToolKeywords(sources...); !eqStrs(got, strs(g(kw, "pool"))) {
		t.Errorf("keywords = %q, want %v", got, g(kw, "pool"))
	}
	for _, r := range rows(fx, "keyword_match") {
		if got := UtteranceMatchesKeywords(gs(r, "utterance"), strs(g(r, "pool"))); got != gb(r, "match") {
			t.Errorf("match(%q) = %v", gs(r, "utterance"), got)
		}
	}
	for _, r := range rows(fx, "normalize_param_type") {
		base, arr, _ := NormalizeParamType(gs(r, "type"))
		if base != gs(r, "base") || arr != gb(r, "is_array") {
			t.Errorf("normalize(%v) = %q,%v", g(r, "type"), base, arr)
		}
	}
	pm := BuildParamMaps(g(rows(fx, "param_commands")[0], "commands").([]any))
	for _, r := range rows(fx, "invalid_params") {
		var calls []ToolCallArgs
		for _, c := range g(r, "calls").([]any) {
			calls = append(calls, ToolCallArgs{Name: gs(c, "name"), Arguments: gs(c, "arguments")})
		}
		if got := FindInvalidParams(calls, pm); !eqStrs(got, strs(g(r, "invalid"))) {
			t.Errorf("invalid(%v)\n got %q\nwant %q", g(r, "calls"), got, strs(g(r, "invalid")))
		}
	}
	nags := rows(fx, "nags")[0]
	longest := func(k string) string {
		best := ""
		for _, s := range strs(g(nags, k)) {
			if len(s) > len(best) {
				best = s
			}
		}
		return best
	}
	checks := map[string]string{
		"must_call_1":  MustCallRetryNag(1),
		"must_call_2":  MustCallRetryNag(2),
		"iso":          ISODateRetryNag,
		"dedupe":       ToolDedupeNag("get_weather"),
		"invalid":      InvalidParamRetryNag(1, 2, []string{"a.b expected int", "c.d must be one of: x, y"}),
		"double_check": NotForMeDoubleCheckPrompt,
	}
	for k, got := range checks {
		if want := longest(k); got != want {
			t.Errorf("nag %s:\n got %q\nwant %q", k, got, want)
		}
	}
	if !IsNag("system", "  [TOOL_DEDUPE] x", TagToolDedupe) || IsNag("user", "[TOOL_DEDUPE]", TagToolDedupe) ||
		IsNag("system", "rules mention [MUST_CALL_RETRY]", TagMustCallRetry) {
		t.Error("IsNag")
	}
}

func outputsOf(v any) []any {
	l, _ := v.([]any)
	if l == nil {
		return []any{}
	}
	return l
}

func TestTextModeFormatGolden(t *testing.T) {
	fx := loadVoice(t, "format.json")
	p, _ := prompts.Lookup(prompts.Qwen3_14B)
	const voiceCommand = "Is it going to rain today?\n/no_think" // the stripped latest user message
	for _, r := range rows(fx, "text_mode") {
		outs := outputsOf(g(r, "outputs"))
		res := g(r, "result")
		if msg, ok := FastPathMessage(outs); ok {
			if g(r, "sent_user") != nil || msg != gs(res, "assistant_message") {
				t.Errorf("%s: fast path %q vs %v", gs(r, "case"), msg, res)
			}
			continue
		}
		ctx := ToolResultsContext(outs)
		prompt := TextModeFormatPrompt(voiceCommand, ctx, IsKnowledgeDelegation(ctx))
		if prompt != gs(r, "sent_user") {
			t.Errorf("%s: prompt\n got %q\nwant %q", gs(r, "case"), prompt, gs(r, "sent_user"))
		}
		text, reasoning := FinishFormattedReply(gs(r, "reply"), p.SanitizeText, outs)
		if text != gs(res, "assistant_message") || reasoning != gs(res, "reasoning") {
			t.Errorf("%s / %q: got %q (%q), want %v", gs(r, "case"), gs(r, "reply"), text, reasoning, res)
		}
	}
}

func TestContinueStreamGolden(t *testing.T) {
	fx := loadVoice(t, "format.json")
	for _, r := range rows(fx, "continue_stream") {
		name := gs(r, "provider") + "/" + gs(r, "case") + "/" + gs(r, "reply")
		native := gs(r, "provider") == "Qwen3_5_9B_Compressed"
		outs := outputsOf(g(r, "outputs"))
		var spoken []string
		lastCommitted := ""
		var llmMsgs []string

		if fast := ContinueStreamFastSpoken(outs); fast != "" {
			for _, p := range SplitSentenceBoundary(fast) {
				if s := parse.PyStrip(p); s != "" {
					spoken = append(spoken, s)
				}
			}
			lastCommitted = fast
		} else {
			ctx := ToolResultsContext(outs)
			if native {
				for _, o := range outs {
					llmMsgs = append(llmMsgs, "tool:"+ToolOutputString(o))
				}
			} else {
				llmMsgs = []string{"user:" + ContinueToolResultsMessage(ctx, IsKnowledgeDelegation(ctx)), "system:" + PlainTextOverride}
			}
			cs := NewContinueStreamer("<think>", "</think>")
			for _, d := range strs(g(r, "deltas")) {
				spoken = append(spoken, cs.Push(d)...)
			}
			tail, commit := cs.Finish()
			if tail != "" {
				spoken = append(spoken, tail)
			}
			if cs.Spoken == 0 {
				if fb := ToolResultFallback(outs); fb != "" {
					for _, p := range SplitSentenceBoundary(fb) {
						if s := parse.PyStrip(p); s != "" {
							spoken = append(spoken, s)
						}
					}
					commit = fb
				}
			}
			lastCommitted = commit
			var want []string
			for _, m := range outputsOf(g(r, "llm_messages")) {
				want = append(want, gs(m, "role")+":"+gs(m, "content"))
			}
			if !eqStrs(llmMsgs, want) {
				t.Errorf("%s: llm messages\n got %q\nwant %q", name, llmMsgs, want)
			}
		}
		if !eqStrs(spoken, strs(g(r, "spoken"))) {
			t.Errorf("%s: spoken\n got %q\nwant %q", name, spoken, strs(g(r, "spoken")))
		}
		committed := outputsOf(g(r, "committed"))
		wantLast := ""
		if len(committed) > 0 {
			last := committed[len(committed)-1]
			if gs(last, "role") == "assistant" {
				wantLast = gs(last, "content")
			}
		}
		if lastCommitted != wantLast {
			t.Errorf("%s: committed %q, want %q", name, lastCommitted, wantLast)
		}
		if native && len(committed) > 0 && lastCommitted != "" {
			// Native commits keep role=tool messages before the assistant reply.
			if n := len(committed); n < 2 || (len(outs) > 0 && gs(committed[n-2], "role") != "tool") {
				t.Errorf("%s: native commit shape %v", name, committed)
			}
		}
	}
}

func TestMaxIterationsFallback(t *testing.T) {
	if MaxIterationsFallback == "Maximum tool execution iterations reached." || !strings.HasSuffix(MaxIterationsFallback, ".") {
		t.Error("D40 Q9: natural fallback")
	}
}

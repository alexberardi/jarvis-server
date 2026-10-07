package hints

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Golden tests against fixtures/golden/voice/hints.json (tools/golden/export_cc_voice.py).
// Absent keys are Python None; hint values index "hint_strings".

func load(t *testing.T) *pyjson.Object {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "voice", "hints.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*pyjson.Object)
}

func get(v any, k string) (any, bool) {
	o, ok := v.(*pyjson.Object)
	if !ok {
		return nil, false
	}
	return o.Get(k)
}

func fptr(v any, k string) *float64 {
	x, ok := get(v, k)
	if !ok || x == nil {
		return nil
	}
	if f, ok := pyFloat(x); ok {
		return &f
	}
	return nil
}

func sptr(v any, k string) *string {
	x, ok := get(v, k)
	if !ok || x == nil {
		return nil
	}
	s := x.(string)
	return &s
}

func bptr(v any, k string) *bool {
	x, ok := get(v, k)
	if !ok || x == nil {
		return nil
	}
	b := x.(bool)
	return &b
}

func iptr(v any, k string) *int {
	x, ok := get(v, k)
	if !ok || x == nil {
		return nil
	}
	f, _ := pyFloat(x)
	i := int(f)
	return &i
}

func hint(t *testing.T, table []any, v any) string {
	x, ok := get(v, "hint")
	if !ok || x == nil {
		return ""
	}
	f, _ := pyFloat(x)
	return table[int(f)].(string)
}

func TestDirectionGolden(t *testing.T) {
	fx := load(t)
	table, _ := fx.Get("hint_strings")
	rows, _ := fx.Get("direction")
	for _, r := range rows.([]any) {
		d := Direction{
			PreWakeSpeechSeconds: fptr(r, "pre_wake_speech_seconds"),
			WakeConfidence:       fptr(r, "wake_confidence"),
			TurnSource:           sptr(r, "turn_source"),
			Transcript:           sptr(r, "transcript"),
			SpeakerKnown:         bptr(r, "speaker_known"),
			SelfPlayback:         bptr(r, "self_playback"),
			SelfPlaybackKind:     sptr(r, "self_playback_kind"),
		}
		if got, want := DirectionHint(d), hint(t, table.([]any), r); got != want {
			t.Fatalf("direction %s:\n got %q\nwant %q", pyjson.Compact(r), got, want)
		}
	}
	t.Logf("%d direction rows", len(rows.([]any)))
}

func TestTurnGolden(t *testing.T) {
	fx := load(t)
	table, _ := fx.Get("hint_strings")
	rows, _ := fx.Get("turn")
	for _, r := range rows.([]any) {
		var names []string
		if l, ok := get(r, "member_names"); ok {
			for _, n := range l.([]any) {
				names = append(names, n.(string))
			}
		}
		tc := Turn{
			TurnSource:              sptr(r, "turn_source"),
			WakeConfidence:          fptr(r, "wake_confidence"),
			FollowUpIteration:       iptr(r, "follow_up_iteration"),
			PreWakeSpeechSeconds:    fptr(r, "pre_wake_speech_seconds"),
			WakeVerified:            bptr(r, "wake_verified"),
			Transcript:              sptr(r, "transcript"),
			SelfPlayback:            bptr(r, "self_playback"),
			SelfPlaybackKind:        sptr(r, "self_playback_kind"),
			ConversationWakeVerdict: sptr(r, "conversation_wake_verdict"),
			DoubtRound:              iptr(r, "doubt_round"),
			DoubtMaxRounds:          iptr(r, "doubt_max_rounds"),
			MemberNames:             names,
		}
		if got, want := TurnHint(tc), hint(t, table.([]any), r); got != want {
			t.Fatalf("turn %s:\n got %q\nwant %q", pyjson.Compact(r), got, want)
		}
		dc, _ := get(r, "double_check")
		if got := ShouldDoubleCheckSentinel(tc); got != dc.(bool) {
			t.Fatalf("double_check %s = %v", pyjson.Compact(r), got)
		}
	}
	t.Logf("%d turn rows", len(rows.([]any)))
}

func TestAffectGolden(t *testing.T) {
	fx := load(t)
	rows, _ := fx.Get("affect")
	for _, r := range rows.([]any) {
		var m map[string]any
		if a, ok := get(r, "affect"); ok {
			if o, ok := a.(*pyjson.Object); ok {
				m = map[string]any{}
				for _, k := range o.Keys() {
					m[k], _ = o.Get(k)
				}
			}
		}
		want := ""
		if h, ok := get(r, "hint"); ok && h != nil {
			want = h.(string)
		}
		if got := AffectHint(m); got != want {
			t.Errorf("affect %s: got %q want %q", pyjson.Compact(r), got, want)
		}
	}
}

func TestProfileGolden(t *testing.T) {
	fx := load(t)
	rows, _ := fx.Get("profile")
	for _, r := range rows.([]any) {
		str := func(k string) string {
			if p := sptr(r, k); p != nil {
				return *p
			}
			return ""
		}
		want := str("hint")
		if got := ProfileMatchHint(str("utterance"), str("block")); got != want {
			t.Errorf("profile %s:\n got %q\nwant %q", pyjson.Compact(r), got, want)
		}
	}
}

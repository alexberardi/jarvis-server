package servertools

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// A desktop agent nobody usable is signed in to says to sign in again, never to enroll a voice
// (decision 2026-10-14, desktop voice speaks as its owner); a Pi keeps the D21 / M14 wording.
func TestUnknownSpeakerWording(t *testing.T) {
	for _, c := range []struct {
		name     string
		sp       Speaker
		refusal  string
		identify string
	}{
		{"pi, recognition on", Speaker{}, "I'm not sure who's speaking.",
			"I don't recognize your voice yet. Enroll a voice profile and I'll know you next time."},
		{"pi, recognition off", Speaker{RecognitionOff: true}, "Speaker recognition is off, so I can't tell who's speaking.",
			"Speaker recognition is off, so I can't tell who's speaking. A household admin can turn it on in settings."},
		{"signed-out computer", Speaker{SignedOutComputer: true}, SignInAgain, SignInAgain},
		{"signed-out computer, recognition off", Speaker{SignedOutComputer: true, RecognitionOff: true}, SignInAgain, SignInAgain},
	} {
		if got := c.sp.Refusal(); got != c.refusal {
			t.Errorf("%s: refusal %q, want %q", c.name, got, c.refusal)
		}
		tr := turn
		tr.Speaker = c.sp
		out, err := NewIdentifySpeaker().Execute(context.Background(), Call{}, tr)
		if err != nil {
			t.Fatal(err)
		}
		msg, _ := out.(*pyjson.Object).Get("message")
		if msg != c.identify {
			t.Errorf("%s: identify_speaker %s, want %q", c.name, dumps(out), c.identify)
		}
	}
	if !strings.Contains(SignInAgain, "Jarvis Automation menu (Set up / Settings…)") || strings.Contains(SignInAgain, "nroll") {
		t.Fatalf("wording %q", SignInAgain)
	}
}

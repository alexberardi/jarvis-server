package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/simline"
)

// On a voice-mode line the persona hears whisper's transcription of Jarvis, while the checks
// and the log keep the exact text.
func TestCalleeHearsTheTranscriptionInVoiceMode(t *testing.T) {
	var prompt []map[string]string
	tap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]string `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompt = body.Messages
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Sure, what time?"}}]}`)
	}))
	defer tap.Close()
	var out bytes.Buffer
	cl := &callee{tap: tap.URL, model: "live", out: &out}
	st := cl.expect("s1", "You are the host.", &out)
	turn, _ := json.Marshal(simline.Turn{SessionID: "s1", Event: "spoke", Voice: true, Said: []string{"A table at 7:30?"},
		Heard: "A table at 730?", Understood: []string{"Papa V's."}})
	rec := httptest.NewRecorder()
	cl.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/turn", bytes.NewReader(turn)))
	var reply simline.Reply
	if err := json.NewDecoder(rec.Body).Decode(&reply); err != nil || reply.Say != "Sure, what time?" {
		t.Fatalf("reply %+v (%v)", reply, err)
	}
	if last := prompt[len(prompt)-1]; last["content"] != "A table at 730?" {
		t.Fatalf("persona heard %q, want the transcription", last["content"])
	}
	if lines, _ := cl.snapshot(st); len(lines) == 0 || lines[0].Who != "jarvis" || lines[0].Text != "A table at 7:30?" {
		t.Fatalf("checks see %+v, want the exact text", lines)
	}
	for _, want := range []string{"jarvis heard: Papa V's.", "🤖 A table at 7:30?", "business heard: A table at 730?"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, out.String())
		}
	}
}

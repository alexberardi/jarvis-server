package simline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

func TestSpokenTextCrossesTheAudioPath(t *testing.T) {
	l := New("http://unused", nil)
	a, _, _ := l.SynthesizePCM(context.Background(), "Hi, this is Jarvis.")
	b, _, _ := l.SynthesizePCM(context.Background(), "A table for two?")
	got := l.spoken(append(append([]int16{}, a...), b...), false)
	if len(got) != 2 || got[0] != "Hi, this is Jarvis." || got[1] != "A table for two?" {
		t.Fatalf("spoken = %q", got)
	}
	id := l.remember("We're full at seven.")
	res, _ := l.Transcribe(context.Background(), live.WAV(speech(id, 4), 8000), sttOpts)
	if res.Text != "We're full at seven." {
		t.Fatalf("heard %q", res.Text)
	}
}

// A call: Jarvis speaks, the driver hears it once Jarvis is quiet, and its reply comes back as
// speech the VAD cuts into one utterance that transcribes to the reply.
func TestCallRoundTrip(t *testing.T) {
	turns := make(chan Turn, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tn Turn
		_ = json.NewDecoder(r.Body).Decode(&tn)
		turns <- tn
		_ = json.NewEncoder(w).Encode(Reply{Say: "Papa V's, how can I help?"})
	}))
	defer srv.Close()
	l := New(srv.URL, nil)
	c, err := l.Dial(context.Background(), phone.DialRequest{SessionID: "s1", To: "+17325550100"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	<-c.Started()
	pcm, _, _ := l.SynthesizePCM(context.Background(), "Hi, I'm an AI assistant calling for Jordan.")
	if _, err := c.Play(pcm, "t0"); err != nil {
		t.Fatal(err)
	}
	vad := live.NewVAD(live.DefaultVADConfig())
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f := <-c.Frames():
			if utt := vad.Feed(f, false); utt != nil {
				res, _ := l.Transcribe(context.Background(), live.WAV(utt, 8000), sttOpts)
				if res.Text != "Papa V's, how can I help?" {
					t.Fatalf("heard %q", res.Text)
				}
				tn := <-turns
				if tn.Event != "spoke" || len(tn.Said) != 1 || tn.Said[0] != "Hi, I'm an AI assistant calling for Jordan." {
					t.Fatalf("driver got %+v", tn)
				}
				return
			}
		case <-deadline:
			t.Fatal("no utterance from the far end")
		}
	}
}

var sttOpts = stt.TranscribeOptions{}

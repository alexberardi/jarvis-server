package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/audio"
)

var testCreds = node.Creds{NodeID: "n1", NodeKey: "secret-key"}

// cc is a fake command-center: it checks the node key on every call and records requests.
func cc(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body []byte)) *HTTPServer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-API-Key") != "n1:secret-key" {
			t.Errorf("%s %s: method or X-API-Key wrong", r.Method, r.URL.Path)
		}
		var body []byte
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			body, _ = io.ReadAll(r.Body)
		}
		h(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return NewHTTPServer(srv.URL+"/", testCreds)
}

func TestStartAndEndConversationWire(t *testing.T) {
	var got []map[string]any
	var paths []string
	s := cc(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		got, paths = append(got, m), append(paths, r.URL.Path)
		if r.URL.Path == "/api/v0/conversation/start" {
			_, _ = w.Write([]byte(`{"status":"success","conversation_id":"c1","home_context":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	ctx := context.Background()
	if err := s.StartConversation(ctx, "c1", map[string]any{"timezone": "America/New_York"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EndConversation(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	want := `[{"available_commands":[],"client_tools":[],"conversation_id":"c1","node_context":{"timezone":"America/New_York"}},{"conversation_id":"c1"}]`
	if b, _ := json.Marshal(got); string(b) != want {
		t.Fatalf("bodies:\n%s\nwant\n%s", b, want)
	}
	if paths[0] != "/api/v0/conversation/start" || paths[1] != "/api/v0/conversation/end" {
		t.Fatalf("paths %v", paths)
	}
}

func TestStartConversationErrorsCarryDetailNotKey(t *testing.T) {
	s := cc(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"Failed to start conversation: boom"}`))
	})
	err := s.StartConversation(context.Background(), "c1", nil)
	var he *node.HTTPError
	if !errors.As(err, &he) || he.Status != 500 || !strings.Contains(he.Detail, "boom") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatal("the node key leaked into an error")
	}
}

func TestTranscribeMultipart(t *testing.T) {
	cmd, spk := []byte("RIFF-command"), []byte("RIFF-wake+command")
	s := cc(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Path != "/api/v0/media/whisper/transcribe" {
			t.Errorf("path %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		for field, want := range map[string][]byte{"file": cmd, "speaker_audio": spk} {
			f, fh, err := r.FormFile(field)
			if err != nil {
				t.Fatalf("%s: %v", field, err)
			}
			b, _ := io.ReadAll(f)
			if !bytes.Equal(b, want) || fh.Header.Get("Content-Type") != "audio/wav" {
				t.Errorf("%s: %q (%s)", field, b, fh.Header.Get("Content-Type"))
			}
		}
		if r.FormValue("conversation_id") != "c1" {
			t.Errorf("conversation_id %q", r.FormValue("conversation_id"))
		}
		_, _ = w.Write([]byte(`{"text":" What time is it?","segments":[],"speaker":{"user_id":1,"confidence":0.8},"affect":{"read":"calm"}}`))
	})
	tr, err := s.Transcribe(context.Background(), cmd, spk, "c1")
	if err != nil || tr.Text != " What time is it?" || tr.Affect["read"] != "calm" {
		t.Fatalf("%+v %v", tr, err)
	}
}

func TestVoiceStreamAudioAndControl(t *testing.T) {
	pcm := []byte{1, 0, 2, 0, 3, 0}
	var req map[string]any
	status := http.StatusOK
	s := cc(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		_ = json.Unmarshal(body, &req)
		if status == http.StatusOK {
			h := w.Header()
			h.Set("Content-Type", "audio/raw")
			h.Set("X-Audio-Sample-Rate", "24000")
			h.Set("X-Audio-Channels", "1")
			h.Set("X-Audio-Sample-Width", "2")
			h.Set("X-Assistant-Message", "It%27s%203%20o%27clock%20%2B%20change.")
			_, _ = w.Write(pcm)
			return
		}
		w.WriteHeader(status)
		if status == http.StatusAccepted {
			_, _ = w.Write([]byte(`{"commands":[],"stop_reason":"not_for_me","tool_calls":[],"assistant_message":null}`))
		} else {
			_, _ = w.Write([]byte(`{"detail":"Conversation not initialized for tool-based flow"}`))
		}
	})
	conf := 0.93
	in := StreamRequest{VoiceCommand: "what time is it", ConversationID: "c1", TurnSource: "wake", WakeConfidence: &conf}
	r, err := s.VoiceStream(context.Background(), in)
	if err != nil || r.Audio == nil {
		t.Fatalf("%+v %v", r, err)
	}
	b, _ := io.ReadAll(r.Audio.Body)
	r.Audio.Close()
	if r.Audio.Rate != 24000 || r.Audio.Channels != 1 || !bytes.Equal(b, pcm) || r.Audio.Message != "It's 3 o'clock + change." {
		t.Fatalf("audio %+v %v", r.Audio, b)
	}
	if req["turn_source"] != "wake" || req["wake_confidence"] != 0.93 || req["conversation_id"] != "c1" || req["voice_command"] != "what time is it" {
		t.Fatalf("request %v", req)
	}
	if _, has := req["affect"]; has {
		t.Fatal("affect must be omitted when STT gave none")
	}

	status = http.StatusAccepted
	r, err = s.VoiceStream(context.Background(), in)
	if err != nil || r.Control == nil || r.Control.StopReason != "not_for_me" || r.Control.AssistantMessage != nil {
		t.Fatalf("202: %+v %v", r, err)
	}

	status = http.StatusBadRequest
	_, err = s.VoiceStream(context.Background(), in)
	var he *node.HTTPError
	if !errors.As(err, &he) || he.Status != 400 || !strings.Contains(he.Detail, "not initialized") {
		t.Fatalf("400: %v", err)
	}
}

func TestSpeakUsesWAVForShortTextAndStreamForLong(t *testing.T) {
	var paths []string
	s := cc(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/stream") {
			w.Header().Set("X-Audio-Sample-Rate", "24000")
			w.Header().Set("X-Audio-Channels", "1")
			w.Header().Set("X-Audio-Sample-Width", "2")
			_, _ = w.Write([]byte{9, 0, 8, 0})
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_ = audio.WritePCM16WAV(w, []byte{5, 0, 6, 0, 7, 0}, 1, 22050)
	})
	a, err := s.Speak(context.Background(), "Sure.")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(a.Body)
	a.Close()
	if a.Rate != 22050 || a.Channels != 1 || !bytes.Equal(b, []byte{5, 0, 6, 0, 7, 0}) {
		t.Fatalf("wav: %+v %v", a, b)
	}
	a, err = s.Speak(context.Background(), strings.Repeat("x", 201))
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(a.Body)
	a.Close()
	if a.Rate != 24000 || !bytes.Equal(b, []byte{9, 0, 8, 0}) {
		t.Fatalf("stream: %+v %v", a, b)
	}
	if paths[0] != "/api/v0/media/tts/speak" || paths[1] != "/api/v0/media/tts/speak/stream" {
		t.Fatalf("paths %v", paths)
	}
}

package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// --- a fake OpenAI-compatible engine behind the real llm.Service ---

// engineReply is one scripted completion.
type engineReply struct {
	content   string
	toolCalls []map[string]any // native tool calls (OpenAI shape)
	finish    string
}

// fakeEngine is an httptest llama-server stand-in: it records every request body and answers
// from a script (the last reply repeats once the script runs out).
type fakeEngine struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	script []engineReply
	reqs   []map[string]any
	delay  time.Duration // added to every completion (span-duration tests)
}

func newFakeEngine(t *testing.T) *fakeEngine {
	f := &fakeEngine{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEngine) push(r ...engineReply) {
	f.mu.Lock()
	f.script = append(f.script, r...)
	f.mu.Unlock()
}

func (f *fakeEngine) say(content string) { f.push(engineReply{content: content}) }

func (f *fakeEngine) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.reqs...)
}

func (f *fakeEngine) last() map[string]any {
	r := f.requests()
	if len(r) == 0 {
		f.t.Fatal("no engine requests")
	}
	return r[len(r)-1]
}

func (f *fakeEngine) next() engineReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.script) == 0 {
		return engineReply{content: "ok"}
	}
	r := f.script[0]
	if len(f.script) > 1 {
		f.script = f.script[1:]
	} else {
		f.script = nil
	}
	return r
}

func (f *fakeEngine) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.reqs = append(f.reqs, body)
	delay := f.delay
	f.mu.Unlock()
	time.Sleep(delay)
	// The warmup (max_tokens 1) never consumes the script.
	if mt, _ := body["max_tokens"].(float64); mt == 1 {
		writeJSON(w, completionBody("", nil, "length"))
		return
	}
	rep := f.next()
	finish := rep.finish
	if finish == "" {
		finish = "stop"
		if len(rep.toolCalls) > 0 {
			finish = "tool_calls"
		}
	}
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, piece := range splitKeep(rep.content) {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": piece}}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
		return
	}
	writeJSON(w, completionBody(rep.content, rep.toolCalls, finish))
}

func completionBody(content string, calls []map[string]any, finish string) map[string]any {
	msg := map[string]any{"role": "assistant", "content": content}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	return map[string]any{"id": "x", "object": "chat.completion", "choices": []any{
		map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}
}

// splitKeep splits text into word-ish stream deltas.
func splitKeep(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s[1:], ' ')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type engineResolver struct{ url string }

func (r engineResolver) Resolve(context.Context, string) (llm.Endpoint, error) {
	return llm.Endpoint{BaseURL: r.url, Model: "fake.gguf", ContextLength: 8192}, nil
}

// messagesOf returns a recorded request's messages as (role, content) pairs.
func messagesOf(req map[string]any) []map[string]any {
	var out []map[string]any
	for _, m := range req["messages"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

// --- fake STT / TTS / notifications / names ---

type fakeSTT struct {
	mu          sync.Mutex
	text        string
	speaker     *int64
	recognition bool
	enrollErr   error
	enrolled    []string // "household:user"
	wakeText    string   // what the wake-clip pass hears ("" = "jarvis")
}

func (f *fakeSTT) Transcribe(_ context.Context, wav []byte, opts stt.TranscribeOptions) (stt.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sp := stt.Speaker{Outcome: stt.OutcomeSkipped}
	if opts.Speaker != nil {
		switch {
		case !f.recognition:
			sp.Outcome = stt.OutcomeOff
		case f.speaker != nil:
			id := *f.speaker
			sp = stt.Speaker{UserID: &id, Confidence: 0.61, Outcome: stt.OutcomeMatched}
		default:
			sp = stt.Speaker{Confidence: 0.2, Outcome: stt.OutcomeNoMatch}
		}
	}
	text := f.text
	if opts.Speaker == nil { // the wake-clip pass
		text = "jarvis"
		if f.wakeText != "" {
			text = f.wakeText
		}
	}
	return stt.Result{Text: text, Segments: []stt.Segment{}, Speaker: sp}, nil
}

func (f *fakeSTT) RecognitionEnabled(context.Context, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recognition
}

func (f *fakeSTT) Enroll(_ context.Context, hh string, uid int64, _ []byte, _ *int) (stt.EnrollResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.enrollErr != nil {
		return stt.EnrollResult{}, f.enrollErr
	}
	f.enrolled = append(f.enrolled, fmt.Sprintf("%s:%d", hh, uid))
	return stt.EnrollResult{Status: "enrolled", UserID: uid, HouseholdID: hh, SampleIndex: len(f.enrolled) - 1, TotalSamples: len(f.enrolled)}, nil
}

func (f *fakeSTT) Verify(_ context.Context, _ string, uid int64, _ []byte) (stt.VerifyResult, error) {
	return stt.VerifyResult{Matched: true, Confidence: 0.7, UserID: uid}, nil
}

// fakeTTS renders each text as its bytes (so tests can read the "audio" back).
type fakeTTS struct {
	mu     sync.Mutex
	spoken []string
}

func (f *fakeTTS) AudioFormat() tts.Format {
	return tts.Format{SampleRate: 24000, Channels: 1, SampleWidth: 2, Provider: "kokoro"}
}

func (f *fakeTTS) Speak(_ context.Context, text string) (Speech, error) {
	if text == "" {
		return nil, tts.ErrNoText
	}
	f.mu.Lock()
	f.spoken = append(f.spoken, text)
	f.mu.Unlock()
	return &fakeSpeech{chunks: [][]byte{[]byte("<" + text + ">")}}, nil
}

func (f *fakeTTS) said() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.spoken...)
}

type fakeSpeech struct{ chunks [][]byte }

func (s *fakeSpeech) Next() ([]byte, error) {
	if len(s.chunks) == 0 {
		return nil, io.EOF
	}
	b := s.chunks[0]
	s.chunks = s.chunks[1:]
	return b, nil
}

func (s *fakeSpeech) Close() error { return nil }

type fakeNotifier struct {
	mu     sync.Mutex
	items  []notifications.NewInboxItem
	pushes []notifications.Notification
}

func (f *fakeNotifier) CreateInboxItem(_ context.Context, _ *sql.Tx, in notifications.NewInboxItem) (notifications.InboxItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = append(f.items, in)
	return notifications.InboxItem{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", len(f.items)), HouseholdID: in.HouseholdID}, nil
}

func (f *fakeNotifier) Notify(_ context.Context, _ *sql.Tx, _ string, n notifications.Notification) (notifications.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, n)
	return notifications.Delivery{ID: "d", DeliveryStatus: "pending"}, nil
}

type fakeNames map[int64]string

func (f fakeNames) UserNames(_ context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	for _, id := range ids {
		if n, ok := f[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

// --- the voice test environment ---

type voiceEnv struct {
	*env
	eng    *fakeEngine
	stt    *fakeSTT
	tts    *fakeTTS
	notify *fakeNotifier
	node   testNode
}

const voiceHH = "hh-voice"

func newVoiceEnv(t *testing.T, provider string, configure ...func(m *Module)) *voiceEnv {
	t.Helper()
	eng := newFakeEngine(t)
	ve := &voiceEnv{eng: eng, stt: &fakeSTT{text: "what time is it"}, tts: &fakeTTS{}, notify: &fakeNotifier{}}
	svc := llm.NewService(llm.ServiceConfig{Resolver: engineResolver{eng.srv.URL}})
	ve.env = newEnv(t, envOpts{noMQTT: true, configure: func(m *Module) {
		m.LLM, m.STT, m.TTS, m.Notify = svc, ve.stt, ve.tts, ve.notify
		m.Names = fakeNames{7: "alex", 8: "sam"}
		for _, c := range configure {
			c(m)
		}
	}})
	ve.set(settingPromptProvider, provider, settings.Scope{})
	ve.auth.addUser(7, voiceHH, "member")
	ve.auth.addUser(8, voiceHH, "member")
	ve.node = ve.createNode("node-v1", voiceHH)
	return ve
}

func (ve *voiceEnv) set(key string, v any, sc settings.Scope) {
	ve.t.Helper()
	if err := ve.m.Settings().Set(context.Background(), key, v, sc); err != nil {
		ve.t.Fatal(err)
	}
}

// start opens a conversation with the given client tools (raw JSON list).
func (ve *voiceEnv) start(cid string, clientTools string, extra ...string) map[string]any {
	ve.t.Helper()
	body := `{"conversation_id":"` + cid + `","node_context":{"timezone":"America/New_York","speaker_user_id":7},"client_tools":` + clientTools
	for _, e := range extra {
		body += "," + e
	}
	body += "}"
	return ve.do("POST", "/api/v0/conversation/start", body, ve.node.h()).want(200).json()
}

func (ve *voiceEnv) turn(path, cid, utterance string, extra map[string]any) *resp {
	ve.t.Helper()
	b := map[string]any{"voice_command": utterance, "conversation_id": cid}
	for k, v := range extra {
		b[k] = v
	}
	return ve.do("POST", path, b, ve.node.h())
}

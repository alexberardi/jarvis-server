package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// --- fakes ---

// square is a 500 Hz square wave of amplitude a: it survives the 16 kHz decimation, and its
// RMS is a.
func square(a int16) []int16 {
	c := make([]int16, ChunkSamples)
	for i := range c {
		if (i/48)%2 == 0 {
			c[i] = a
		} else {
			c[i] = -a
		}
	}
	return c
}

var (
	noise  = square(50)   // the room: RMS 50, so the adaptive threshold is the 200 floor
	speech = square(2000) // the command
	wake   = square(5000) // what the fake detector calls "hey jarvis"
)

// script is the microphone's audio: the given chunks, then noise forever. The rig runs the
// pipeline in lockstep with it, so nothing is dropped however slowly the pipeline runs; pace
// only keeps the endless noise from spinning a CPU.
type fakeCapture struct {
	mu     sync.Mutex
	chunks [][]int16
	pace   time.Duration
	err    error // returned once the script is used up, if set
	reads  int
}

func (c *fakeCapture) Read(buf []int16) error {
	time.Sleep(c.pace)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	if len(c.chunks) == 0 {
		if c.err != nil {
			return c.err
		}
		copy(buf, noise)
		return nil
	}
	copy(buf, c.chunks[0])
	c.chunks = c.chunks[1:]
	return nil
}
func (c *fakeCapture) Overruns() int { return 0 }
func (c *fakeCapture) Close() error  { return nil }

func script(parts ...[][]int16) *fakeCapture {
	return &fakeCapture{chunks: slices.Concat(parts...), pace: 2 * time.Millisecond}
}

func (c *fakeCapture) readCount() int { c.mu.Lock(); defer c.mu.Unlock(); return c.reads }

// push queues more audio after whatever is left of the script.
func (c *fakeCapture) push(parts ...[][]int16) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chunks = append(c.chunks, slices.Concat(parts...)...)
}

// fakeDetector fires on loud audio.
type fakeDetector struct {
	mu     sync.Mutex
	resets int
}

func (d *fakeDetector) Process(c []int16) (float32, error) {
	if len(c) != wakeChunk {
		return 0, errors.New("bad chunk size")
	}
	var sum float64
	for _, v := range c {
		sum += float64(max(v, -v))
	}
	if sum/float64(len(c)) > 3000 {
		return 0.95, nil
	}
	return 0.01, nil
}
func (d *fakeDetector) Reset() { d.mu.Lock(); d.resets++; d.mu.Unlock() }

type played struct {
	rate, channels int
	pcm            []int16
	drained        bool
}

type speaker struct {
	mu   sync.Mutex
	outs []*played
}

type fakePlayback struct {
	s *speaker
	p *played
}

func (f fakePlayback) Write(b []int16) error {
	f.s.mu.Lock()
	f.p.pcm = append(f.p.pcm, b...)
	f.s.mu.Unlock()
	return nil
}
func (f fakePlayback) Drain() error { f.s.mu.Lock(); f.p.drained = true; f.s.mu.Unlock(); return nil }
func (f fakePlayback) Close() error { return nil }

func (s *speaker) open(rate, ch int) (device.Playback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &played{rate: rate, channels: ch}
	s.outs = append(s.outs, p)
	return fakePlayback{s, p}, nil
}

func (s *speaker) all() []played {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []played
	for _, p := range s.outs {
		out = append(out, *p)
	}
	return out
}

// fakeServer is command-center.
type fakeServer struct {
	mu       sync.Mutex
	calls    []string
	startErr error
	text     string
	reply    func(ctx context.Context) (Reply, error)
	speakPCM []byte
	// speakBody, when set, is Speak's audio instead of speakPCM.
	speakBody func(ctx context.Context, text string) io.ReadCloser
	// cont answers /voice/command/continue; nil means "complete" with nothing to say.
	cont func(results []ToolResult) (*Control, error)
	// texts are the transcripts of the recordings after the first, in order (then text).
	texts []string
	// followUp answers the blocking /voice/command; nil means "complete" with nothing to say.
	followUp    func(req StreamRequest) (*Control, error)
	commands    []StreamRequest // every /voice/command body, in order
	said        []string        // every Speak text, in order
	transcribed int

	nodeCtx  map[string]any
	tools    ToolSchemas
	results  [][]ToolResult // every continue's tool_results, in order
	cmdWAV   []byte
	spkWAV   []byte
	streamIn StreamRequest
	spoken   string
	ended    chan string
	onSpeak  func(text string) // called before Speak answers
}

func newServer() *fakeServer {
	return &fakeServer{text: "What time is it?", ended: make(chan string, 4),
		reply: func(context.Context) (Reply, error) { return audioReply(24000, "It is three.", pcmOf(2400)), nil }}
}

func (s *fakeServer) log(c string) { s.mu.Lock(); s.calls = append(s.calls, c); s.mu.Unlock() }

func (s *fakeServer) StartConversation(_ context.Context, id string, nc map[string]any, tools ToolSchemas) error {
	s.log("start")
	s.mu.Lock()
	s.nodeCtx, s.tools = nc, tools
	s.mu.Unlock()
	return s.startErr
}
func (s *fakeServer) EndConversation(_ context.Context, id string) error {
	s.log("end")
	s.ended <- id
	return nil
}
func (s *fakeServer) Transcribe(_ context.Context, cmd, spk []byte, id string) (Transcript, error) {
	s.log("transcribe")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcribed++
	if s.transcribed == 1 {
		s.cmdWAV, s.spkWAV = cmd, spk
	}
	text := s.text
	if s.transcribed > 1 && len(s.texts) > 0 {
		text, s.texts = s.texts[0], s.texts[1:]
	}
	return Transcript{Text: text, Affect: map[string]any{"read": "calm"}}, nil
}
func (s *fakeServer) VoiceStream(ctx context.Context, req StreamRequest) (Reply, error) {
	s.log("stream")
	s.mu.Lock()
	s.streamIn = req
	s.mu.Unlock()
	return s.reply(ctx)
}
func (s *fakeServer) Speak(ctx context.Context, text string) (*Audio, error) {
	s.log("speak")
	s.mu.Lock()
	s.spoken = text
	s.said = append(s.said, text)
	onSpeak := s.onSpeak
	body := io.NopCloser(bytes.NewReader(s.speakPCM))
	if s.speakBody != nil {
		body = s.speakBody(ctx, text)
	}
	s.mu.Unlock()
	if onSpeak != nil {
		onSpeak(text)
	}
	return &Audio{Rate: 22050, Channels: 1, Message: text, Body: body}, nil
}

func (s *fakeServer) ContinueTools(_ context.Context, id string, results []ToolResult) (*Control, error) {
	s.log("continue")
	s.mu.Lock()
	s.results = append(s.results, results)
	cont := s.cont
	s.mu.Unlock()
	if cont == nil {
		return &Control{StopReason: "complete"}, nil
	}
	return cont(results)
}

func (s *fakeServer) VoiceCommand(_ context.Context, req StreamRequest) (*Control, error) {
	s.log("command")
	s.mu.Lock()
	s.commands = append(s.commands, req)
	f := s.followUp
	s.mu.Unlock()
	if f == nil {
		return &Control{StopReason: "complete"}, nil
	}
	return f(req)
}

func (s *fakeServer) callList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func pcmOf(n int) []byte {
	b := make([]byte, 2*n)
	for i := range n {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(i%1000)))
	}
	return b
}

func audioReply(rate int, msg string, pcm []byte) Reply {
	return Reply{Audio: &Audio{Rate: rate, Channels: 1, Message: msg, Body: io.NopCloser(bytes.NewReader(pcm))}}
}

func control(stop string, msg *string) func(context.Context) (Reply, error) {
	return func(context.Context) (Reply, error) {
		return Reply{Control: &Control{StopReason: stop, AssistantMessage: msg}}, nil
	}
}

type rig struct {
	p     *Pipeline
	srv   *fakeServer
	spk   *speaker
	det   *fakeDetector
	turns chan Turn
}

func newRig(t *testing.T, mic device.Capture, srv *fakeServer) *rig {
	t.Helper()
	r := &rig{srv: srv, spk: &speaker{}, det: &fakeDetector{}, turns: make(chan Turn, 4)}
	cfg := DefaultConfig()
	cfg.Timezone = "America/New_York"
	r.p = &Pipeline{Cfg: cfg, Capture: mic, Playback: r.spk.open, Detector: r.det, Server: srv,
		Log:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		OnTurn: func(tr Turn) { r.turns <- tr }, NewID: func() string { return "conv-1" }, lockstep: true}
	return r
}

// run starts the pipeline and returns a stop function that waits for Run to return.
func (r *rig) run(t *testing.T) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancel")
			return nil
		}
	}
}

func (r *rig) turn(t *testing.T) Turn {
	t.Helper()
	select {
	case tr := <-r.turns:
		return tr
	case <-time.After(10 * time.Second):
		t.Fatal("no voice turn")
		return Turn{}
	}
}

func (r *rig) waitResets(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.det.mu.Lock()
		got := r.det.resets
		r.det.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("detector reset %d times; want %d", got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// noTurnBy fails if a turn has been reported once the pipeline has taken mic chunk n. In
// lockstep the mic's read n+2 starts only after the pipeline took chunk n+1, i.e. finished
// with chunk n and any turn it fired (OnTurn runs on the pipeline goroutine).
func (r *rig) noTurnBy(t *testing.T, mic *fakeCapture, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second) // a hang guard, not what is measured
	for mic.readCount() < n+2 {
		if time.Now().After(deadline) {
			t.Fatalf("the pipeline stalled at mic read %d", mic.readCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case tr := <-r.turns:
		t.Fatalf("unexpected turn %+v", tr)
	default:
	}
}

// oneWake is: 3.2 s of room, "hey jarvis", a 1 s command, then room noise.
func oneWake() *fakeCapture {
	return script(rep(noise, 40), rep(wake, 3), rep(speech, 12))
}

func decodeWAV(t *testing.T, b []byte) ([]int16, int) {
	t.Helper()
	s, rate, err := audio.ReadWAV(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]int16, len(s))
	for i, v := range s {
		out[i] = int16(v * 32768)
	}
	return out, rate
}

// --- tests ---

func TestTurnStreamsTheReplyToTheSpeaker(t *testing.T) {
	r := newRig(t, oneWake(), newServer())
	stop := r.run(t)
	tr := r.turn(t)
	// OnTurn fires before the pipeline goes back to listening; cancelling now could beat
	// the resume reset checked below.
	r.waitResets(t, 2)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if tr.Outcome != OutcomeSpoken || tr.Err != nil || tr.Transcript != "What time is it?" || tr.Message != "It is three." {
		t.Fatalf("turn %+v", tr)
	}
	if tr.WakeScore != 0.95 || tr.Threshold != 200 || tr.ConversationID != "conv-1" {
		t.Fatalf("wake score / adaptive threshold / id: %+v", tr)
	}
	// The command ran until 0.8 s of silence after at least 2 s.
	if tr.HitMax || tr.RecordedSecs < 2 || tr.RecordedSecs > 2.5 {
		t.Fatalf("recorded %.2f s (hitMax %v), want ~2.08 s", tr.RecordedSecs, tr.HitMax)
	}

	// Wire: start (with the timezone), transcribe, stream, end; end only after start.
	if got := r.srv.callList(); !slices.Equal(got, []string{"start", "transcribe", "stream", "end"}) &&
		!slices.Equal(got, []string{"transcribe", "start", "stream", "end"}) {
		t.Fatalf("calls %v", got)
	}
	if r.srv.nodeCtx["timezone"] != "America/New_York" {
		t.Fatalf("node_context %v", r.srv.nodeCtx)
	}
	in := r.srv.streamIn
	if in.TurnSource != "wake" || in.WakeConfidence == nil || *in.WakeConfidence != float64(float32(0.95)) ||
		in.ConversationID != "conv-1" || in.VoiceCommand != "What time is it?" || in.Affect["read"] != "calm" {
		t.Fatalf("stream request %+v", in)
	}

	// Uploads: 48 kHz command; speaker_audio = the 2 s wake clip (ending in the wake word),
	// then the command.
	cmd, rate := decodeWAV(t, r.srv.cmdWAV)
	spk, rate2 := decodeWAV(t, r.srv.spkWAV)
	clip := wakeClipChunks * ChunkSamples
	if rate != 48000 || rate2 != 48000 || len(spk) != clip+len(cmd) || !slices.Equal(spk[clip:], cmd) {
		t.Fatalf("command %d samples @%d, speaker %d @%d", len(cmd), rate, len(spk), rate2)
	}
	if !slices.Equal(spk[clip-ChunkSamples:clip], wake) || !slices.Equal(spk[:ChunkSamples], noise) {
		t.Fatal("the wake clip should be room audio ending in the chunk that fired")
	}

	// Speaker: the wake chime, then the reply at the stream's own rate, drained.
	outs := r.spk.all()
	if len(outs) != 2 {
		t.Fatalf("%d playbacks, want chime + reply", len(outs))
	}
	if outs[0].rate != chimeRate || !slices.Equal(outs[0].pcm, wakeChime) {
		t.Fatalf("first playback is not the wake chime")
	}
	if outs[1].rate != 24000 || len(outs[1].pcm) != 2400 || !outs[1].drained || tr.ReplySecs != 0.1 {
		t.Fatalf("reply playback %d Hz, %d samples, drained %v, %.3f s", outs[1].rate, len(outs[1].pcm), outs[1].drained, tr.ReplySecs)
	}
	r.det.mu.Lock()
	defer r.det.mu.Unlock()
	if r.det.resets < 2 {
		t.Fatalf("detector reset %d times; want after the fire and again on resuming", r.det.resets)
	}
}

func TestStreamedReplySplitAcrossOddReads(t *testing.T) {
	srv := newServer()
	pcm := pcmOf(1001)
	srv.reply = func(context.Context) (Reply, error) {
		// One byte at a time: samples straddle reads.
		return Reply{Audio: &Audio{Rate: 24000, Channels: 1, Body: io.NopCloser(&oneByte{b: pcm})}}, nil
	}
	r := newRig(t, oneWake(), srv)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	outs := r.spk.all()
	if tr.Outcome != OutcomeSpoken || len(outs) != 2 || len(outs[1].pcm) != 1001 {
		t.Fatalf("%+v, playbacks %d", tr, len(outs))
	}
	for i, v := range outs[1].pcm {
		if v != int16(i%1000) {
			t.Fatalf("sample %d = %d", i, v)
		}
	}
}

type oneByte struct{ b []byte }

func (o *oneByte) Read(p []byte) (int, error) {
	if len(o.b) == 0 {
		return 0, io.EOF
	}
	p[0] = o.b[0]
	o.b = o.b[1:]
	return 1, nil
}

func TestControlReplies(t *testing.T) {
	say := "Sure, done."
	empty := "  "
	cases := []struct {
		name     string
		reply    func(context.Context) (Reply, error)
		outcome  Outcome
		calls    []string
		playback []int // sample counts after the wake chime
		errChime bool
	}{
		{"not_for_me is silent", control("not_for_me", nil), OutcomeNotForMe, []string{"stream", "end"}, nil, false},
		{"complete is spoken via TTS", control("complete", &say), OutcomeSpoken, []string{"stream", "speak", "end"}, []int{300}, false},
		{"complete with nothing to say is silent", control("complete", &empty), OutcomeSilent, []string{"stream", "end"}, nil, false},
		{"validation without a question is an error", control("validation_required", nil), OutcomeError, []string{"stream", "end"}, nil, true},
		{"error with a message speaks it", control("error", &say), OutcomeError, []string{"stream", "speak", "end"}, []int{300}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newServer()
			srv.reply = c.reply
			srv.speakPCM = pcmOf(300)
			r := newRig(t, oneWake(), srv)
			stop := r.run(t)
			tr := r.turn(t)
			_ = stop()
			if tr.Outcome != c.outcome {
				t.Fatalf("outcome %s (%v), want %s", tr.Outcome, tr.Err, c.outcome)
			}
			calls := r.srv.callList()
			if i := slices.Index(calls, "stream"); i < 0 || !slices.Equal(calls[i:], c.calls) {
				t.Fatalf("calls %v, want ... %v", calls, c.calls)
			}
			outs := r.spk.all()[1:]
			var got []int
			for _, o := range outs {
				if c.errChime && slices.Equal(o.pcm, errorChime) {
					continue
				}
				got = append(got, len(o.pcm))
			}
			if !slices.Equal(got, c.playback) {
				t.Fatalf("playbacks %v, want %v", got, c.playback)
			}
			if c.errChime != (len(outs) > 0 && slices.Equal(outs[len(outs)-1].pcm, errorChime)) {
				t.Fatalf("error chime played = %v, want %v", !c.errChime, c.errChime)
			}
			if c.outcome == OutcomeSpoken && r.srv.spoken != say {
				t.Fatalf("spoke %q", r.srv.spoken)
			}
		})
	}
}

func TestNonSpeechSkipsTheTurnButEndsTheConversation(t *testing.T) {
	srv := newServer()
	srv.text = " [BLANK_AUDIO]"
	r := newRig(t, oneWake(), srv)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Outcome != OutcomeNonSpeech || slices.Contains(r.srv.callList(), "stream") {
		t.Fatalf("%+v calls %v", tr, r.srv.callList())
	}
	if !slices.Contains(r.srv.callList(), "end") {
		t.Fatal("conversation/end must always be sent")
	}
	if len(r.spk.all()) != 1 {
		t.Fatal("non-speech should play nothing after the wake chime")
	}
}

func TestStartFailurePlaysTheErrorChime(t *testing.T) {
	srv := newServer()
	srv.startErr = errors.New("warmup failed")
	r := newRig(t, oneWake(), srv)
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Outcome != OutcomeError || !strings.Contains(tr.Err.Error(), "warmup failed") {
		t.Fatalf("%+v", tr)
	}
	if slices.Contains(r.srv.callList(), "stream") || !slices.Contains(r.srv.callList(), "end") {
		t.Fatalf("calls %v", r.srv.callList())
	}
	outs := r.spk.all()
	if !slices.Equal(outs[len(outs)-1].pcm, errorChime) {
		t.Fatal("no error chime")
	}
}

// blockingBody never sends anything until the request context is cancelled.
type blockingBody struct{ ctx context.Context }

func (b blockingBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b blockingBody) Close() error             { return nil }

func TestStalledReplyStreamTimesOut(t *testing.T) {
	srv := newServer()
	srv.reply = func(ctx context.Context) (Reply, error) {
		return Reply{Audio: &Audio{Rate: 24000, Channels: 1, Body: blockingBody{ctx}}}, nil
	}
	r := newRig(t, oneWake(), srv)
	r.p.Cfg.StreamIdle = 50 * time.Millisecond
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Outcome != OutcomeError || tr.Err == nil || !strings.Contains(tr.Err.Error(), "idle") {
		t.Fatalf("%+v", tr)
	}
}

func TestDebounceAndResumeListening(t *testing.T) {
	// Two wakes 0.8 s of audio apart (well after the first turn has finished).
	mic := script(rep(noise, 40), rep(wake, 3), rep(speech, 20), rep(noise, 300), rep(wake, 3), rep(speech, 20))

	// The clock stands still: the second wake is inside the 8 s debounce.
	r := newRig(t, mic, newServer())
	fixed := time.Unix(1000, 0)
	r.p.Now = func() time.Time { return fixed }
	stop := r.run(t)
	r.turn(t)
	r.noTurnBy(t, mic, 40+3+20+300+3+20)
	_ = stop()

	// The clock moves 10 s per reading: both fire.
	mic = script(rep(noise, 40), rep(wake, 3), rep(speech, 20), rep(noise, 300), rep(wake, 3), rep(speech, 20))
	r = newRig(t, mic, newServer())
	var mu sync.Mutex
	clock := time.Unix(1000, 0)
	r.p.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); clock = clock.Add(10 * time.Second); return clock }
	stop = r.run(t)
	r.turn(t)
	r.turn(t)
	_ = stop()
}

func TestMicrophoneFailureEndsRun(t *testing.T) {
	mic := script(rep(noise, 5))
	mic.err = errors.New("device unplugged")
	r := newRig(t, mic, newServer())
	err := r.p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "device unplugged") {
		t.Fatalf("Run = %v", err)
	}
}

func TestQuietRoomUsesStaticThresholdAndRecordsTheMinimum(t *testing.T) {
	silent := make([]int16, ChunkSamples)
	mic := script(rep(silent, 40), rep(wake, 3))
	mic.chunks = append(mic.chunks, rep(silent, 100)...)
	r := newRig(t, mic, newServer())
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	if tr.Threshold != 300 {
		t.Fatalf("threshold %v, want the static 300 when the room reads 0", tr.Threshold)
	}
	if tr.RecordedSecs != 26*chunkSecs {
		t.Fatalf("recorded %.2f s, want the 2 s minimum (26 chunks)", tr.RecordedSecs)
	}
}

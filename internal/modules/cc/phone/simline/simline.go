// Package simline is a simulated phone line for dev sandboxes (JARVIS_DEV_PHONE_SIM): the call
// path runs as on a real call (disclosure, VAD, turn loop, hang-up rules, outcome and cards),
// but the far end is a driver reached over HTTP (tools/pipeline call) that reads what Jarvis
// said as text and answers in text.
//
// Text crosses the audio path as tagged PCM. The Line is also the phone service's TTS: each
// synthesized text becomes a short buffer that starts with a marker naming it, so Play can tell
// what was said. The far end's replies are played into Frames as a square wave whose amplitude
// names the reply, and the Line as STT reads that amplitude back. Both directions are 8 kHz, as
// the call loop uses them, so the samples arrive unchanged.
//
// Voice mode (Line.Voice, JARVIS_DEV_PHONE_SIM_AUDIO=voice) carries real audio instead. The
// Line stays the phone service's TTS and STT, but as a pass-through to the sandbox's own:
// Jarvis's replies are real TTS at 8 kHz (with the marker in front, so the exact text still
// travels for logs and checks) and play out in real time, so barge-in and Flush work as on
// SIP. What reached the far end's ear is transcribed with the sandbox's whisper at 8 kHz and
// sent as Turn.Heard. The driver's replies are synthesized in another voice, resampled to
// 8 kHz and played into Frames in real time, for Jarvis's own VAD, STT and turn loop.
package simline

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/modules/tts"
)

const (
	rate       = 8000
	frameLen   = 160 // 20 ms
	markerA    = 31111
	markerTail = 12345
	// The far end's speech amplitude is ampBase + its reply's id (ids stay below ampSpan).
	ampBase = 4000
	ampSpan = 20000
	// quietAfterJarvis: Jarvis has finished a reply once nothing new has played for this long.
	quietAfterJarvis = 1500 * time.Millisecond
	// silenceNudge: with nothing said for this long, the far end is told so ("Hello?").
	silenceNudge = 20 * time.Second
	// FarVoice is the far end's Kokoro voice in voice mode (Jarvis's default is bm_george).
	FarVoice = "af_heart"
)

// Turn is what the line posts to the driver: what Jarvis said since the far end last spoke.
// Event is "spoke" (Jarvis finished a reply), "silence" (nothing for a while) or "ended" (the call
// is over; no answer is read).
type Turn struct {
	SessionID string `json:"session_id"`
	To        string `json:"to"`
	Event     string `json:"event"`
	// Said is the exact text Jarvis's TTS was given. In voice mode it is for logs and checks
	// only: the far end did not hear it, it heard Heard.
	Said []string `json:"said"`
	// Voice: the line carries real audio, and Heard and Understood are set.
	Voice bool `json:"voice,omitempty"`
	// Heard is the sandbox's whisper transcription of Jarvis's audio as the far end heard it
	// (8 kHz, cut short by a barge-in); "" when nothing intelligible arrived.
	Heard string `json:"heard,omitempty"`
	// Understood is what Jarvis's STT made of the far end's speech since the last turn.
	Understood []string `json:"understood,omitempty"`
}

// Reply is the driver's answer: what the far end says next ("" says nothing), and whether it
// then hangs up.
type Reply struct {
	Say    string `json:"say"`
	Hangup bool   `json:"hangup"`
}

// Voice is the sandbox's real speech, for voice mode.
type Voice struct {
	STT phone.STT
	// TTS renders text in a Kokoro voice ("" is the sandbox's configured one, Jarvis's).
	TTS      func(ctx context.Context, text, voice string) (pcm []int16, rate int, err error)
	FarVoice string // the far end's voice
}

// SpeakWith adapts the tts module to Voice.TTS.
func SpeakWith(m *tts.Module) func(context.Context, string, string) ([]int16, int, error) {
	return func(ctx context.Context, text, voice string) ([]int16, int, error) {
		st, err := m.Speak(ctx, text, tts.SpeakOptions{Voice: voice})
		if err != nil {
			return nil, 0, err
		}
		defer st.Close()
		b, err := io.ReadAll(st)
		return live.PCMBytesToInt16(b), st.Format.SampleRate, err
	}
}

// Line is the provider, TTS and STT of simulated calls.
type Line struct {
	URL    string // the driver's base URL; turns go to URL + "/turn"
	Log    *slog.Logger
	Voice  *Voice // nil: text mode
	client *http.Client

	// Tests: tick replaces the 20 ms ticker, now the wall clock, and asked is called once a
	// driver reply has been queued.
	tick  <-chan time.Time
	now   func() time.Time
	asked func()

	mu         sync.Mutex
	texts      map[int]string // id → text, both directions
	next       int
	understood []string // voice mode: Jarvis's transcriptions not yet reported
}

// New returns a line whose far end is the driver at url.
func New(url string, log *slog.Logger) *Line {
	return &Line{URL: strings.TrimRight(url, "/"), Log: log, client: &http.Client{Timeout: 3 * time.Minute},
		texts: map[int]string{}}
}

func (l *Line) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *Line) remember(text string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	if l.next >= ampSpan {
		l.next = 1
	}
	l.texts[l.next] = text
	return l.next
}

func (l *Line) text(id int) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.texts[id]
	return t, ok
}

// SynthesizePCM is the phone service's TTS: a marker naming text, at 8 kHz; in voice mode the
// marker is followed by the text's real speech.
func (l *Line) SynthesizePCM(ctx context.Context, text string) ([]int16, int, error) {
	n, var8k := frameLen, []int16(nil)
	if l.Voice != nil {
		pcm, r, err := l.Voice.TTS(ctx, text, "")
		if err != nil {
			return nil, 0, err
		}
		// shortcut: the 4 marker samples are a 0.5 ms click in the call recording (the far
		// end's transcription gets them zeroed); drop them if a recording check ever minds.
		n, var8k = 4, live.Resample(pcm, r, rate)
	}
	id := l.remember(text)
	pcm := append(make([]int16, n), var8k...)
	pcm[0], pcm[1], pcm[2], pcm[3] = markerA, -markerA, int16(id), markerTail
	return pcm, rate, nil
}

// spoken is the texts whose markers pcm holds, in order; unmark zeroes the markers.
func (l *Line) spoken(pcm []int16, unmark bool) []string {
	var out []string
	for i := 0; i+3 < len(pcm); i++ {
		if pcm[i] == markerA && pcm[i+1] == -markerA && pcm[i+3] == markerTail {
			if t, ok := l.text(int(pcm[i+2])); ok {
				out = append(out, t)
			}
			if unmark {
				pcm[i], pcm[i+1], pcm[i+2], pcm[i+3] = 0, 0, 0, 0
			}
			i += 3
		}
	}
	return out
}

// Transcribe is the phone service's STT: the reply the utterance's amplitude names, or in
// voice mode the sandbox's STT, noted for the driver.
func (l *Line) Transcribe(ctx context.Context, wav []byte, opts stt.TranscribeOptions) (stt.Result, error) {
	if l.Voice != nil {
		res, err := l.Voice.STT.Transcribe(ctx, wav, opts)
		if err == nil {
			// shortcut: kept per line, not per call; a sandbox runs one simulated call at a time.
			l.mu.Lock()
			l.understood = append(l.understood, res.Text)
			l.mu.Unlock()
		}
		return res, err
	}
	if len(wav) <= 44 {
		return stt.Result{}, nil
	}
	counts := map[int]int{}
	for i := 44; i+1 < len(wav); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(wav[i:])))
		if v < 0 {
			v = -v
		}
		if v > ampBase && v < ampBase+ampSpan {
			counts[v-ampBase]++
		}
	}
	best, n := 0, 0
	for id, c := range counts {
		if c > n {
			best, n = id, c
		}
	}
	t, _ := l.text(best)
	return stt.Result{Text: t, Engine: "simline"}, nil
}

// speech is the far end saying the reply id: a square wave long enough for the VAD, about as
// long as saying it would take (the VAD cuts an utterance at 15 s).
func speech(id int, words int) []int16 {
	d := min(max(time.Duration(words)*300*time.Millisecond, 800*time.Millisecond), 12*time.Second)
	n := int(d.Seconds() * rate)
	n -= n % frameLen
	amp := int16(ampBase + id)
	pcm := make([]int16, n)
	for i := range pcm {
		if (i/40)%2 == 0 {
			pcm[i] = amp
		} else {
			pcm[i] = -amp
		}
	}
	return pcm
}

// Ready: the driver is reached per turn.
func (l *Line) Ready() error {
	if l.URL == "" {
		return errors.New("simulated phone line: no driver URL")
	}
	return nil
}

// LineType: a simulated business line.
func (l *Line) LineType(context.Context, string) string { return "landline" }

// Dial answers at once.
func (l *Line) Dial(_ context.Context, req phone.DialRequest) (phone.Call, error) {
	c := &call{line: l, req: req, started: make(chan struct{}), done: make(chan struct{}),
		frames: make(chan []int16), played: closedCh()}
	if l.Voice != nil {
		c.played = make(chan struct{})
		// The last call's final line, transcribed after its "ended" turn, is not this call's.
		l.mu.Lock()
		l.understood = nil
		l.mu.Unlock()
	}
	close(c.started)
	go c.run()
	return c, nil
}

func closedCh() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// call is one simulated call. In text mode playback is instant: Played is always at the frame
// clock. In voice mode Jarvis's audio queues in out and plays a frame per tick into ear.
type call struct {
	line    *Line
	req     phone.DialRequest
	started chan struct{}
	done    chan struct{}
	frames  chan []int16
	played  chan struct{}

	mu        sync.Mutex
	pos       int64     // samples since Started
	said      []string  // what Jarvis said since the driver last heard
	lastPlay  time.Time // when Jarvis last played something
	lastHeard time.Time // when the far end last finished speaking, or the call started
	queue     []int16   // the far end's speech still to send
	asking    bool      // a driver request is out
	hangAfter bool      // the far end hangs up once its queue is sent
	out       []int16   // voice mode: Jarvis's audio still to play
	ear       []int16   // voice mode: Jarvis's audio the far end has heard since it last spoke
	reason    phone.EndReason
	endOnce   sync.Once
}

var _ phone.Call = (*call)(nil)

func (c *call) ID() string                   { return "sim-" + c.req.SessionID }
func (c *call) Started() <-chan struct{}     { return c.started }
func (c *call) Frames() <-chan []int16       { return c.frames }
func (c *call) Done() <-chan struct{}        { return c.done }
func (c *call) SendDTMF(string) error        { return phone.ErrUnsupported }
func (c *call) Hangup(context.Context) error { c.end(phone.EndClosed); return nil }
func (c *call) Close()                       { c.end(phone.EndClosed) }
func (c *call) Played() (int64, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pos, c.played
}

// Flush drops what has not played (voice mode), as a SIP pacer: Played jumps to its end.
func (c *call) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.out) > 0 {
		c.pos += int64(len(c.out))
		c.out = nil
		c.advanced()
	}
	return nil
}

// advanced signals Played's change (c.mu held; voice mode).
func (c *call) advanced() {
	close(c.played)
	c.played = make(chan struct{})
}

func (c *call) EndReason() phone.EndReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// Play hears what Jarvis said; it has played by the time Play returns. In voice mode it is
// queued and plays in real time.
func (c *call) Play(pcm []int16, _ string) (int64, error) {
	if c.line.Voice != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.out = append(c.out, pcm...)
		c.lastPlay = c.line.clock()
		return c.pos + int64(len(c.out)), nil
	}
	texts := c.line.spoken(pcm, false)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.said = append(c.said, texts...)
	c.lastPlay = time.Now()
	return c.pos, nil
}

// take is the turn to post: what Jarvis said since the far end last heard (exact text) and, in
// voice mode, the transcription of what reached the far end's ear and what Jarvis made of it.
func (c *call) take(ctx context.Context, event string) Turn {
	c.mu.Lock()
	said, ear := c.said, c.ear
	c.said, c.ear = nil, nil
	c.mu.Unlock()
	t := Turn{SessionID: c.req.SessionID, To: c.req.To, Event: event, Said: said}
	v := c.line.Voice
	if v == nil {
		return t
	}
	t.Voice, t.Said = true, c.line.spoken(ear, true)
	c.line.mu.Lock()
	t.Understood, c.line.understood = c.line.understood, nil
	c.line.mu.Unlock()
	if len(ear) > 0 {
		res, err := v.STT.Transcribe(ctx, live.WAV(ear, rate), stt.TranscribeOptions{})
		if err != nil && c.line.Log != nil {
			c.line.Log.Warn("simline: transcribing Jarvis failed — the far end heard nothing", "session", c.req.SessionID, "err", err)
		}
		t.Heard = strings.TrimSpace(res.Text)
	}
	return t
}

func (c *call) end(r phone.EndReason) {
	c.endOnce.Do(func() {
		c.mu.Lock()
		c.reason = r
		c.mu.Unlock()
		close(c.done)
		go func() { _, _ = c.line.post(c.take(context.Background(), "ended")) }()
	})
}

// run is the frame clock: 20 ms frames in real time, the far end's speech or silence, and the
// driver asked for a reply once Jarvis has finished speaking.
func (c *call) run() {
	defer close(c.frames)
	c.mu.Lock()
	c.lastHeard = c.line.clock()
	c.mu.Unlock()
	ticks := c.line.tick
	if ticks == nil {
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		ticks = t.C
	}
	for {
		select {
		case <-c.done:
			return
		case <-ticks:
		}
		frame := make([]int16, frameLen)
		c.mu.Lock()
		if len(c.out) > 0 {
			n := min(frameLen, len(c.out))
			c.ear = append(c.ear, c.out[:n]...)
			c.out = c.out[n:]
			c.lastPlay = c.line.clock()
		}
		if len(c.queue) > 0 {
			copy(frame, c.queue[:frameLen])
			c.queue = c.queue[frameLen:]
			if len(c.queue) == 0 {
				c.lastHeard = c.line.clock()
				if c.hangAfter {
					c.mu.Unlock()
					c.end(phone.EndRemote)
					return
				}
			}
		}
		c.pos += frameLen
		if c.line.Voice != nil {
			c.advanced()
		}
		event := c.due()
		c.mu.Unlock()
		select {
		case c.frames <- frame:
		case <-c.done:
			return
		}
		if event != "" {
			go c.ask(event)
		}
	}
}

// due is the driver event to send now, if any (c.mu held): "spoke" once Jarvis has said
// something and gone quiet, "silence" once nobody has said anything for a while.
func (c *call) due() string {
	if c.asking || len(c.queue) > 0 || len(c.out) > 0 || c.hangAfter {
		return ""
	}
	now := c.line.clock()
	spoke := len(c.said) > 0 || len(c.ear) > 0
	if spoke && now.Sub(c.lastPlay) >= quietAfterJarvis {
		c.asking = true
		return "spoke"
	}
	if !spoke && now.Sub(c.lastHeard) >= silenceNudge && now.Sub(c.lastPlay) >= silenceNudge {
		c.asking = true
		return "silence"
	}
	return ""
}

func (c *call) ask(event string) {
	if c.line.asked != nil {
		defer c.line.asked()
	}
	r, err := c.line.post(c.take(context.Background(), event))
	var pcm []int16
	if err == nil && strings.TrimSpace(r.Say) != "" {
		pcm, err = c.speak(r.Say)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asking = false
	c.lastHeard = c.line.clock()
	if err != nil {
		if c.line.Log != nil {
			c.line.Log.Warn("simline: driver failed — the far end stays silent", "session", c.req.SessionID, "err", err)
		}
		return
	}
	c.queue = append(c.queue, pcm...)
	if r.Hangup {
		c.hangAfter = true
		if len(c.queue) == 0 {
			go c.end(phone.EndRemote)
		}
	}
}

// speak is the far end saying text: tagged PCM, or in voice mode its real speech at 8 kHz in
// whole frames.
func (c *call) speak(text string) ([]int16, error) {
	v := c.line.Voice
	if v == nil {
		return speech(c.line.remember(text), len(strings.Fields(text))), nil
	}
	pcm, r, err := v.TTS(context.Background(), text, v.FarVoice)
	if err != nil {
		return nil, fmt.Errorf("far end's TTS: %w", err)
	}
	pcm = live.Resample(pcm, r, rate)
	if n := len(pcm) % frameLen; n > 0 {
		pcm = append(pcm, make([]int16, frameLen-n)...)
	}
	return pcm, nil
}

func (l *Line) post(t Turn) (Reply, error) {
	body, _ := json.Marshal(t)
	resp, err := l.client.Post(l.URL+"/turn", "application/json", bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Reply{}, fmt.Errorf("driver: %s", resp.Status)
	}
	var r Reply
	if t.Event == "ended" {
		return r, nil
	}
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

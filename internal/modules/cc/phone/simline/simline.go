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
package simline

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
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
)

// Turn is what the line posts to the driver: what Jarvis said since the far end last spoke.
// Event is "spoke" (Jarvis finished a reply), "silence" (nothing for a while) or "ended" (the call
// is over; no answer is read).
type Turn struct {
	SessionID string   `json:"session_id"`
	To        string   `json:"to"`
	Event     string   `json:"event"`
	Said      []string `json:"said"`
}

// Reply is the driver's answer: what the far end says next ("" says nothing), and whether it
// then hangs up.
type Reply struct {
	Say    string `json:"say"`
	Hangup bool   `json:"hangup"`
}

// Line is the provider, TTS and STT of simulated calls.
type Line struct {
	URL    string // the driver's base URL; turns go to URL + "/turn"
	Log    *slog.Logger
	client *http.Client

	mu    sync.Mutex
	texts map[int]string // id → text, both directions
	next  int
}

// New returns a line whose far end is the driver at url.
func New(url string, log *slog.Logger) *Line {
	return &Line{URL: strings.TrimRight(url, "/"), Log: log, client: &http.Client{Timeout: 3 * time.Minute},
		texts: map[int]string{}}
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

// SynthesizePCM is the phone service's TTS: a marker naming text, at 8 kHz.
func (l *Line) SynthesizePCM(_ context.Context, text string) ([]int16, int, error) {
	id := l.remember(text)
	pcm := make([]int16, frameLen)
	pcm[0], pcm[1], pcm[2], pcm[3] = markerA, -markerA, int16(id), markerTail
	return pcm, rate, nil
}

// spoken is the texts whose markers pcm holds, in order.
func (l *Line) spoken(pcm []int16) []string {
	var out []string
	for i := 0; i+3 < len(pcm); i++ {
		if pcm[i] == markerA && pcm[i+1] == -markerA && pcm[i+3] == markerTail {
			if t, ok := l.text(int(pcm[i+2])); ok {
				out = append(out, t)
			}
			i += 3
		}
	}
	return out
}

// Transcribe is the phone service's STT: the reply the utterance's amplitude names.
func (l *Line) Transcribe(_ context.Context, wav []byte, _ stt.TranscribeOptions) (stt.Result, error) {
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
	close(c.started)
	go c.run()
	return c, nil
}

func closedCh() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// call is one simulated call. Playback is instant: Played is always at the frame clock.
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
	reason    phone.EndReason
	endOnce   sync.Once
}

var _ phone.Call = (*call)(nil)

func (c *call) ID() string                   { return "sim-" + c.req.SessionID }
func (c *call) Started() <-chan struct{}     { return c.started }
func (c *call) Frames() <-chan []int16       { return c.frames }
func (c *call) Done() <-chan struct{}        { return c.done }
func (c *call) Flush() error                 { return nil }
func (c *call) SendDTMF(string) error        { return phone.ErrUnsupported }
func (c *call) Hangup(context.Context) error { c.end(phone.EndClosed); return nil }
func (c *call) Close()                       { c.end(phone.EndClosed) }
func (c *call) Played() (int64, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pos, c.played
}

func (c *call) EndReason() phone.EndReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// Play hears what Jarvis said; it has played by the time Play returns.
func (c *call) Play(pcm []int16, _ string) (int64, error) {
	texts := c.line.spoken(pcm)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.said = append(c.said, texts...)
	c.lastPlay = time.Now()
	return c.pos, nil
}

func (c *call) end(r phone.EndReason) {
	c.endOnce.Do(func() {
		c.mu.Lock()
		c.reason = r
		said := c.said
		c.said = nil
		c.mu.Unlock()
		close(c.done)
		go func() { _, _ = c.line.post(Turn{SessionID: c.req.SessionID, To: c.req.To, Event: "ended", Said: said}) }()
	})
}

// run is the frame clock: 20 ms frames in real time, the far end's speech or silence, and the
// driver asked for a reply once Jarvis has finished speaking.
func (c *call) run() {
	defer close(c.frames)
	c.mu.Lock()
	c.lastHeard = time.Now()
	c.mu.Unlock()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-tick.C:
		}
		frame := make([]int16, frameLen)
		c.mu.Lock()
		if len(c.queue) > 0 {
			copy(frame, c.queue[:frameLen])
			c.queue = c.queue[frameLen:]
			if len(c.queue) == 0 {
				c.lastHeard = time.Now()
				if c.hangAfter {
					c.mu.Unlock()
					c.end(phone.EndRemote)
					return
				}
			}
		}
		c.pos += frameLen
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
	if c.asking || len(c.queue) > 0 || c.hangAfter {
		return ""
	}
	now := time.Now()
	if len(c.said) > 0 && now.Sub(c.lastPlay) >= quietAfterJarvis {
		c.asking = true
		return "spoke"
	}
	if len(c.said) == 0 && now.Sub(c.lastHeard) >= silenceNudge && now.Sub(c.lastPlay) >= silenceNudge {
		c.asking = true
		return "silence"
	}
	return ""
}

func (c *call) ask(event string) {
	c.mu.Lock()
	said := c.said
	c.said = nil
	c.mu.Unlock()
	r, err := c.line.post(Turn{SessionID: c.req.SessionID, To: c.req.To, Event: event, Said: said})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asking = false
	c.lastHeard = time.Now()
	if err != nil {
		if c.line.Log != nil {
			c.line.Log.Warn("simline: driver failed — the far end stays silent", "session", c.req.SessionID, "err", err)
		}
		return
	}
	if strings.TrimSpace(r.Say) != "" {
		c.queue = append(c.queue, speech(c.line.remember(r.Say), len(strings.Fields(r.Say)))...)
	}
	if r.Hangup {
		c.hangAfter = true
		if len(c.queue) == 0 {
			go c.end(phone.EndRemote)
		}
	}
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

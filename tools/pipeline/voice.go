package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The voice loop: a turn's text is synthesized by the sandbox's own TTS, transcribed by its
// STT through command-center's node media proxy (what a Pi posts after the wake word), and the
// transcript, not the text, becomes the voice command. It shows what Jarvis hears; it needs a
// sandbox started with --voice.

// VoiceLog is what a voiced turn heard and what it cost.
type VoiceLog struct {
	Voice        string  `json:"voice,omitempty"` // the Kokoro voice; "" = the house voice (tts.kokoro_voice)
	Heard        string  `json:"heard"`
	WER          float64 `json:"wer"` // word error rate of Heard against the said text
	Speaker      string  `json:"speaker_outcome,omitempty"`
	AudioSeconds float64 `json:"audio_seconds"`
	TTSSeconds   float64 `json:"tts_seconds"`
	STTSeconds   float64 `json:"stt_seconds"`

	WAV []byte `json:"-"` // the synthesized audio, for --out
}

// Transcript is the fields of command-center's transcription (stt.Result) the runner reads.
type Transcript struct {
	Text    string `json:"text"`
	Speaker struct {
		Outcome string `json:"outcome"`
	} `json:"speaker"`
}

// Speak synthesizes text in the house voice (M1, node auth): a RIFF WAV.
func (n *Node) Speak(ctx context.Context, text string) ([]byte, error) {
	b, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	return n.send(ctx, "/api/v0/media/tts/speak", "application/json", b)
}

// Transcribe posts audio as a node does after the wake word (M3). With a conversation id the
// speaker pass is recorded on that conversation, as on a Pi.
func (n *Node) Transcribe(ctx context.Context, cid string, wav []byte) (Transcript, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "command.wav")
	if err != nil {
		return Transcript{}, err
	}
	fw.Write(wav)
	if cid != "" {
		mw.WriteField("conversation_id", cid)
	}
	if err := mw.Close(); err != nil {
		return Transcript{}, err
	}
	rb, err := n.send(ctx, "/api/v0/media/whisper/transcribe", mw.FormDataContentType(), body.Bytes())
	if err != nil {
		return Transcript{}, err
	}
	var t Transcript
	if err := json.Unmarshal(rb, &t); err != nil {
		return Transcript{}, fmt.Errorf("transcribe: decode: %w", err)
	}
	t.Text = strings.TrimSpace(t.Text)
	return t, nil
}

// AdminVoice renders a named Kokoro voice through the admin's voice preview (POST
// /api/tts/sample, superuser), the only route that takes a voice per request. It logs in once.
type AdminVoice struct {
	AdminURL, AuthURL string
	Email, Password   string

	mu    sync.Mutex
	token string
}

// sampleLimit is the admin preview's text cap (admin.maxSampleChars).
const sampleLimit = 200

func (a *AdminVoice) Speak(ctx context.Context, voice, text string) ([]byte, error) {
	if len([]rune(text)) > sampleLimit {
		return nil, fmt.Errorf("--voice-name: %d characters, the voice preview takes %d (drop the voice name to use the house voice)", len([]rune(text)), sampleLimit)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token == "" {
		if a.AuthURL == "" || a.Email == "" {
			return nil, errors.New("--voice-name: the sandbox exports no SBX_AUTH / SBX_EMAIL (scripts/sandbox up again)")
		}
		var lr struct {
			AccessToken string `json:"access_token"`
		}
		if err := postJSON(ctx, a.AuthURL+"/auth/login", nil, map[string]string{"email": a.Email, "password": a.Password}, &lr); err != nil {
			return nil, err
		}
		a.token = lr.AccessToken
	}
	b, err := json.Marshal(map[string]string{"voice": voice, "text": text})
	if err != nil {
		return nil, err
	}
	return do(ctx, nil, a.AdminURL+"/api/tts/sample", "application/json", b, map[string]string{"Authorization": "Bearer " + a.token})
}

// hear runs the voice loop for one utterance: synthesize, transcribe as the node.
func (d *Driver) hear(ctx context.Context, cid, say, voice string) (*VoiceLog, error) {
	vl := &VoiceLog{Voice: voice}
	start := time.Now()
	var err error
	if voice == "" {
		vl.WAV, err = d.Node.Speak(ctx, say)
	} else if d.Voices == nil {
		err = errors.New("--voice-name needs the sandbox's admin (no AdminVoice)")
	} else {
		vl.WAV, err = d.Voices.Speak(ctx, voice, say)
	}
	if err != nil {
		return nil, fmt.Errorf("tts: %w%s", err, noVoiceHint(err))
	}
	vl.TTSSeconds = time.Since(start).Seconds()
	vl.AudioSeconds = wavSeconds(vl.WAV)
	// whisper-server (re)starting is a 503 "not ready": wait for it rather than fail the turn,
	// which is what a person would do (a setting change restarts it).
	var tr Transcript
	for waited := time.Duration(0); ; waited += engineRetry {
		start = time.Now()
		tr, err = d.Node.Transcribe(ctx, cid, vl.WAV)
		if err == nil || !strings.Contains(err.Error(), "not ready") || waited >= engineWait {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(engineRetry):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("stt: %w%s", err, noVoiceHint(err))
	}
	vl.STTSeconds = time.Since(start).Seconds()
	vl.Heard, vl.Speaker = tr.Text, tr.Speaker.Outcome
	vl.WER = wordErrorRate(say, tr.Text)
	return vl, nil
}

// engineWait bounds waiting for a starting STT engine; engineRetry is the poll (tests lower it).
var engineWait, engineRetry = 2 * time.Minute, time.Second

// noVoiceHint says what a 503 or 409 from TTS or STT usually means here: no voice models.
func noVoiceHint(err error) string {
	s := err.Error()
	if (strings.Contains(s, ": 503 ") || strings.Contains(s, ": 409 ")) && !strings.Contains(s, "not ready") {
		return " (is the sandbox up with --voice?)"
	}
	return ""
}

// wavSeconds is a PCM WAV's duration from its fmt and data chunks; 0 when it can't tell.
func wavSeconds(b []byte) float64 {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0
	}
	var byteRate, data uint32
	for p := 12; p+8 <= len(b); {
		id, size := string(b[p:p+4]), le32(b[p+4:p+8])
		body := p + 8
		switch {
		case id == "fmt " && body+12 <= len(b):
			byteRate = le32(b[body+8 : body+12])
		case id == "data":
			data = size
			if rest := uint32(len(b) - body); data > rest {
				data = rest
			}
		}
		p = body + int(size) + int(size&1)
	}
	if byteRate == 0 {
		return 0
	}
	return float64(data) / float64(byteRate)
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// words splits text for WER: lower case, punctuation dropped (an apostrophe inside a word
// stays, "what's"), so "Set a timer." and "set a timer" are the same words. Numbers are not
// normalized: "12" against "twelve" is an error.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(s, "’", "'")), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
}

// wordErrorRate is the word-level edit distance from said to heard over said's word count.
func wordErrorRate(said, heard string) float64 {
	a, b := words(said), words(heard)
	for i := range b {
		b[i] = strings.Trim(b[i], "'")
	}
	for i := range a {
		a[i] = strings.Trim(a[i], "'")
	}
	if len(a) == 0 {
		if len(b) == 0 {
			return 0
		}
		return 1
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			sub := prev[j-1]
			if a[i-1] != b[j-1] {
				sub++
			}
			cur[j] = min(sub, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return float64(prev[len(b)]) / float64(len(a))
}

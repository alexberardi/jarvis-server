//go:build contract

package contract

import (
	"net/http"
	"strconv"
	"testing"
)

// jarvis-tts (PLAN Phase 0 item 4: voice PCM stream headers). Nodes never call TTS directly;
// command-center's media proxy and voice-stream routes do, with app-to-app credentials, and
// copy the X-Audio-* headers through to the node, which initialises playback from them
// (jarvis-node-setup tts_providers/jarvis_tts_api.py: rate/channels/width, defaults
// 22050/1/2). docs/cc/06 §2.1 M1/M2. Bodies are checked for framing, never for audio content.

const ttsText = "Contract test."

// ttsAudioFormat is GET /audio/format, which CC's voice-stream routes read to set X-Audio-*
// without synthesising (docs/cc/01 §3.3 step 7).
var ttsAudioFormat = Obj{
	"sample_rate":  Int,
	"channels":     Int,
	"sample_width": Int,
	"provider":     NonEmptyString,
}

func ttsFormat(t *testing.T, app *App) (rate, channels, width int, provider string) {
	t.Helper()
	tg := T(t)
	r := tg.Get(t, TTS, "/audio/format", app.H()).Expect(http.StatusOK, ttsAudioFormat)
	var f struct {
		SampleRate  int    `json:"sample_rate"`
		Channels    int    `json:"channels"`
		SampleWidth int    `json:"sample_width"`
		Provider    string `json:"provider"`
	}
	r.Decode(&f)
	if f.SampleRate <= 0 || f.Channels != 1 || f.SampleWidth != 2 {
		r.Fatalf("audio format: want mono s16 at a positive rate, got %+v", f)
	}
	t.Logf("tts format: %+v", f)
	return f.SampleRate, f.Channels, f.SampleWidth, f.Provider
}

func TestTTSAudioFormat(t *testing.T) {
	tg := T(t)
	tg.Need(t, TTS)
	ttsFormat(t, SharedApp(t))
}

// TestTTSSpeakStream freezes POST /speak/stream: 200 audio/raw, chunked, headerless s16le PCM,
// with X-Audio-Sample-Rate/Channels/Sample-Width as decimal strings that agree with
// /audio/format, plus X-Audio-Provider (which CC does not forward). X-Assistant-Message is a
// command-center header (docs/cc/01 §3.3) and TTS never sets it.
func TestTTSSpeakStream(t *testing.T) {
	tg := T(t)
	tg.Need(t, TTS)
	app := SharedApp(t)
	rate, channels, width, provider := ttsFormat(t, app)

	ctx := H{ // what CC sends alongside the app credentials (docs/cc/06 §3.1)
		"X-Context-Household-Id": "contract-household",
		"X-Context-Node-Id":      "contract-node",
	}
	r := tg.SlowJSON(t, TTS, "/speak/stream", map[string]string{"text": ttsText}, app.H(), ctx)
	r.ExpectStatus(http.StatusOK)
	r.ExpectMediaType("audio/raw")
	r.ExpectChunked()
	r.ExpectHeaderVal("X-Audio-Sample-Rate", strconv.Itoa(rate))
	r.ExpectHeaderVal("X-Audio-Channels", strconv.Itoa(channels))
	r.ExpectHeaderVal("X-Audio-Sample-Width", strconv.Itoa(width))
	r.ExpectHeaderVal("X-Audio-Provider", provider)
	for _, h := range []string{"X-Audio-Sample-Rate", "X-Audio-Channels", "X-Audio-Sample-Width"} {
		if _, ok := atoiHeader(r.Resp, h); !ok {
			r.Fatalf("%s is not a decimal integer: %q", h, r.HeaderVal(h))
		}
	}
	if r.HasHeader("X-Assistant-Message") {
		r.Fatalf("TTS must not set X-Assistant-Message (a command-center header)")
	}
	frame := channels * width
	if len(r.Body) == 0 || len(r.Body)%frame != 0 {
		r.Fatalf("PCM body: %d bytes is not a positive whole number of %d-byte frames", len(r.Body), frame)
	}
	// Headerless: raw PCM must not start with a RIFF header.
	if len(r.Body) >= 4 && string(r.Body[:4]) == "RIFF" {
		r.Fatalf("stream body carries a WAV header; it must be raw PCM")
	}
}

// TestTTSSpeak freezes POST /speak: 200 audio/wav, a complete RIFF WAV (not streamed) whose fmt
// chunk matches /audio/format, 16-bit PCM, data a whole number of frames.
func TestTTSSpeak(t *testing.T) {
	tg := T(t)
	tg.Need(t, TTS)
	app := SharedApp(t)
	rate, channels, width, _ := ttsFormat(t, app)

	r := tg.SlowJSON(t, TTS, "/speak", map[string]string{"text": ttsText}, app.H())
	r.ExpectStatus(http.StatusOK).ExpectMediaType("audio/wav")
	info, err := ParseWAV(r.Body)
	if err != nil {
		r.Fatalf("not a WAV: %v", err)
	}
	if info.Format != 1 || info.Channels != channels || info.SampleRate != rate || info.BitsPerSample != 8*width {
		r.Fatalf("WAV fmt %+v disagrees with /audio/format (%d Hz, %d ch, %d bytes)", info, rate, channels, width)
	}
	if info.DataLen == 0 || info.DataLen != info.DataAvailable || info.DataLen%(channels*width) != 0 {
		r.Fatalf("WAV data: declared %d, present %d, frame %d", info.DataLen, info.DataAvailable, channels*width)
	}
}

// TestTTSEmptyText freezes the empty-text answer on both synthesis routes.
func TestTTSEmptyText(t *testing.T) {
	tg := T(t)
	tg.Need(t, TTS)
	app := SharedApp(t)
	for _, path := range []string{"/speak", "/speak/stream"} {
		t.Run(path, func(t *testing.T) {
			// LEGACY-BUG: empty text is HTTP 200 with a JSON error body (TTS app/main.py
			// speak/speak_stream), which CC then relabels audio/wav or audio/raw (docs/cc/06
			// §8 item 7). D8: Go answers 400 JSON.
			tg.Post(t, TTS, path, map[string]string{"text": ""}, app.H()).
				Expect(http.StatusOK, Obj{"error": Eq("No text provided")})
			tg.Post(t, TTS, path, map[string]string{}, app.H()).
				Expect(http.StatusOK, Obj{"error": Eq("No text provided")})
		})
	}
}

// TestTTSAppAuth freezes jarvis-auth-client's require_app_auth answers on every TTS route CC
// calls.
func TestTTSAppAuth(t *testing.T) {
	tg := T(t)
	tg.Need(t, TTS)
	app := SharedApp(t)
	bad := H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong-" + randHex(8)}
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/audio/format"},
		{http.MethodPost, "/speak"},
		{http.MethodPost, "/speak/stream"},
	} {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			var body any
			if rt.method == http.MethodPost {
				body = map[string]string{"text": ttsText}
			}
			tg.Do(t, TTS, rt.method, rt.path, body).ExpectError(http.StatusUnauthorized, "Missing app credentials")
			tg.Do(t, TTS, rt.method, rt.path, body, H{"X-Jarvis-App-Id": app.ID}).
				ExpectError(http.StatusUnauthorized, "Missing app credentials")
			tg.Do(t, TTS, rt.method, rt.path, body, bad).ExpectError(http.StatusUnauthorized, "Invalid app credentials")
		})
	}
}

package voice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRecorder struct {
	takes []Take
	pcm   []int16
	err   error
}

func (r *fakeRecorder) Record(_ context.Context, t Take) ([]int16, error) {
	r.takes = append(r.takes, t)
	if r.err != nil {
		return nil, r.err
	}
	return r.pcm, nil
}

// profileCC is command-center's M4, M5 and V9 as httptest: it records every upload and result.
type profileCC struct {
	mu      sync.Mutex
	uploads []upload
	results map[string]map[string]any
	status  int    // M4/M5's status (200 if 0)
	body    string // M4/M5's answer
}

type upload struct {
	path, rawQuery, field, contentType string
	wav                                []byte
}

func newProfileCC(t *testing.T, status int, body string) (*profileCC, *HTTPServer) {
	p := &profileCC{results: map[string]map[string]any{}, status: status, body: body}
	s := cc(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if rid, ok := strings.CutPrefix(r.URL.Path, "/api/v0/mobile/voice-profile-results/"); ok {
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Errorf("result body: %v", err)
			}
			p.results[rid] = m
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		part, err := mr.NextPart()
		if err != nil {
			t.Errorf("%s: no multipart part: %v", r.URL.Path, err)
			return
		}
		wav, _ := io.ReadAll(part)
		p.uploads = append(p.uploads, upload{r.URL.Path, r.URL.RawQuery, part.FormName(), part.Header.Get("Content-Type"), wav})
		if p.status != 0 {
			w.WriteHeader(p.status)
		}
		_, _ = w.Write([]byte(p.body))
	})
	return p, s
}

func profiles(rec Recorder) *VoiceProfiles {
	return &VoiceProfiles{Mic: rec, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func details(rid string) map[string]any {
	// As the dispatcher decodes V7/V8's publish.
	return map[string]any{"user_id": float64(7), "household_id": "hh-1", "prompt_text": "The quick brown fox",
		"duration_secs": float64(8), "request_id": rid}
}

func TestEnrollRecordsUploadsAndPostsTheResult(t *testing.T) {
	rec := &fakeRecorder{pcm: make([]int16, 2*CaptureRate)}
	p, srv := newProfileCC(t, 0, `{"status":"enrolled","user_id":7,"household_id":"hh-1","sample_index":1,"total_samples":1}`)
	profiles(rec).Enroll(context.Background(), srv, details("r1"))

	if len(rec.takes) != 1 || rec.takes[0].Secs != 8 || rec.takes[0].Preroll != 1500*time.Millisecond ||
		rec.takes[0].Cue != "Read the prompt on your screen, starting now." {
		t.Fatalf("takes = %+v", rec.takes)
	}
	if len(p.uploads) != 1 {
		t.Fatalf("uploads = %+v", p.uploads)
	}
	u := p.uploads[0]
	if u.path != "/api/v0/media/whisper/voice-profiles/enroll" || u.rawQuery != "user_id=7" || u.field != "file" || u.contentType != "audio/wav" {
		t.Fatalf("upload = %s?%s field %q type %q", u.path, u.rawQuery, u.field, u.contentType)
	}
	if string(u.wav[:4]) != "RIFF" || len(u.wav) != 44+2*2*CaptureRate {
		t.Fatalf("upload is not a 2 s 48 kHz WAV (%d bytes)", len(u.wav))
	}
	got, _ := json.Marshal(p.results["r1"])
	want := `{"duration_secs":2,"response":{"household_id":"hh-1","sample_index":1,"status":"enrolled","total_samples":1,"user_id":7},"success":true,"user_id":7}`
	if string(got) != want {
		t.Fatalf("result:\n%s\nwant\n%s", got, want)
	}
}

func TestVerifyRecordsUploadsAndPostsMatch(t *testing.T) {
	rec := &fakeRecorder{pcm: make([]int16, CaptureRate)}
	p, srv := newProfileCC(t, 0, `{"matched":true,"confidence":0.82,"user_id":7}`)
	det := details("r2")
	delete(det, "duration_secs") // the default is the verb's: 5 s
	profiles(rec).Verify(context.Background(), srv, det)

	if len(rec.takes) != 1 || rec.takes[0].Secs != 5 {
		t.Fatalf("takes = %+v", rec.takes)
	}
	u := p.uploads[0]
	if u.path != "/api/v0/media/whisper/voice-profiles/verify" || u.rawQuery != "household_id=hh-1&user_id=7" || u.field != "file" {
		t.Fatalf("upload = %s?%s field %q", u.path, u.rawQuery, u.field)
	}
	got, _ := json.Marshal(p.results["r2"])
	if want := `{"confidence":0.82,"matched":true,"success":true}`; string(got) != want {
		t.Fatalf("result:\n%s\nwant\n%s", got, want)
	}
}

func TestVoiceProfileFailures(t *testing.T) {
	cases := []struct {
		name, verb  string
		recErr      error
		status      int
		body        string
		errorPrefix string
	}{
		{"no pipeline", "enroll_voice", ErrMicUnavailable, 0, "", "audio_bus_unavailable"},
		{"mic fails", "enroll_voice", errors.New("no audio from the microphone for 1 s"), 0, "", "record_failed: no audio"},
		// A take M4 rejects as low quality: jarvisd's sink turns upload_failed into low_quality.
		{"low quality", "enroll_voice", nil, 422, `{"success":false,"error":"low_quality","detail":"too quiet"}`, "upload_failed: "},
		{"no profile", "verify_voice", nil, 404, `{"detail":"No voice profile enrolled"}`, "verify_failed: "},
		{"verify mic", "verify_voice", errors.New("unplugged"), 0, "", "record_failed: unplugged"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &fakeRecorder{pcm: make([]int16, CaptureRate), err: c.recErr}
			p, srv := newProfileCC(t, c.status, c.body)
			vp := profiles(rec)
			if c.verb == "enroll_voice" {
				vp.Enroll(context.Background(), srv, details("r"))
			} else {
				vp.Verify(context.Background(), srv, details("r"))
			}
			res := p.results["r"]
			if res["success"] != false || !strings.HasPrefix(res["error"].(string), c.errorPrefix) {
				t.Fatalf("result = %v, want error %q…", res, c.errorPrefix)
			}
			if c.recErr != nil && len(p.uploads) != 0 {
				t.Fatal("a failed take was uploaded")
			}
		})
	}
}

func TestVoiceProfileWithoutIdsDoesNothing(t *testing.T) {
	for _, missing := range []string{"user_id", "household_id", "request_id"} {
		rec := &fakeRecorder{pcm: make([]int16, CaptureRate)}
		p, srv := newProfileCC(t, 0, `{}`)
		det := details("r")
		det[missing] = nil
		profiles(rec).Enroll(context.Background(), srv, det)
		profiles(rec).Verify(context.Background(), srv, det)
		if len(rec.takes)+len(p.uploads)+len(p.results) != 0 {
			t.Fatalf("without %s: takes %d, uploads %d, results %v", missing, len(rec.takes), len(p.uploads), p.results)
		}
	}
}

func TestVoiceProfileBoundsTheDuration(t *testing.T) {
	rec := &fakeRecorder{pcm: make([]int16, CaptureRate)}
	_, srv := newProfileCC(t, 0, `{}`)
	det := details("r")
	det["duration_secs"] = float64(3600)
	profiles(rec).Enroll(context.Background(), srv, det)
	if rec.takes[0].Secs != maxTakeSecs {
		t.Fatalf("secs = %v, want the %v s cap", rec.takes[0].Secs, maxTakeSecs)
	}
}

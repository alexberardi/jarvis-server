package cc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// The media proxy (docs/cc/06 §2.1): nodes' only audio backend, now in process — no HTTP hop,
// no X-Context-* headers. The transcription also feeds the conversation's identity (D3) and
// the wake-clip verdict (D40 01.Q6, computed in process). Plus the node-mic enrollment
// handoff: V7/V8 start a take on a node, M4/M5 must match a pending start for that node and
// user and use the node's household (D4 06.Q5), V9 (the shared result sink) delivers, V10
// polls.

const (
	maxAudioUpload  = 25 << 20
	wakeSliceSecs   = 2.2
	enrollmentTTL   = 5 * time.Minute
	enrollResultTTL = 5 * time.Minute
)

// --- M1 / M2: TTS ---

func readSpeakText(w http.ResponseWriter, r *http.Request) (string, bool) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return "", false
	}
	text, _ := b.str("text", true)
	if !b.done(w) {
		return "", false
	}
	return text, true
}

// handleTTSSpeak is M1: a complete RIFF WAV at the engine's real rate. Empty text after
// cleaning is a 400 (06 §11; legacy served a JSON error labelled audio/wav).
func (m *Module) handleTTSSpeak(w http.ResponseWriter, r *http.Request, _ *nodeCtx) {
	text, ok := readSpeakText(w, r)
	if !ok {
		return
	}
	if m.TTS == nil {
		detail(w, http.StatusServiceUnavailable, "TTS unavailable")
		return
	}
	text = cleanForTTS(text)
	if text == "" {
		detail(w, http.StatusBadRequest, "No text provided")
		return
	}
	st, err := m.TTS.Speak(r.Context(), text)
	if err != nil {
		m.ttsError(w, err)
		return
	}
	defer st.Close()
	var pcm bytes.Buffer
	for {
		b, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			m.ttsError(w, err)
			return
		}
		pcm.Write(b)
	}
	f := m.TTS.AudioFormat()
	var out bytes.Buffer
	_ = audio.WritePCM16WAV(&out, pcm.Bytes(), f.Channels, f.SampleRate)
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", strconv.Itoa(out.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Bytes())
}

// handleTTSSpeakStream is M2: headerless s16le PCM with the X-Audio-* headers.
func (m *Module) handleTTSSpeakStream(w http.ResponseWriter, r *http.Request, _ *nodeCtx) {
	text, ok := readSpeakText(w, r)
	if !ok {
		return
	}
	if m.TTS == nil {
		detail(w, http.StatusServiceUnavailable, "TTS unavailable")
		return
	}
	text = cleanForTTS(text)
	if text == "" {
		detail(w, http.StatusBadRequest, "No text provided")
		return
	}
	h := w.Header()
	f := m.TTS.AudioFormat()
	st, err := m.TTS.Speak(r.Context(), text)
	if err != nil {
		m.ttsError(w, err)
		return
	}
	defer st.Close()
	h.Set("Content-Type", "audio/raw")
	h.Set("X-Audio-Sample-Rate", strconv.Itoa(f.SampleRate))
	h.Set("X-Audio-Channels", strconv.Itoa(f.Channels))
	h.Set("X-Audio-Sample-Width", strconv.Itoa(f.SampleWidth))
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	for {
		b, err := st.Next()
		if err != nil {
			return
		}
		if _, err := w.Write(b); err != nil {
			return
		}
		_ = rc.Flush()
	}
}

func (m *Module) ttsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tts.ErrNoText):
		detail(w, http.StatusBadRequest, "No text provided")
	case errors.Is(err, tts.ErrNotInstalled):
		detail(w, http.StatusServiceUnavailable, err.Error())
	default:
		m.deps.Log.Error("cc: TTS failed", "err", err)
		detail(w, http.StatusInternalServerError, "TTS failed")
	}
}

// --- multipart helpers ---

// parseUpload parses a multipart body; ok=false means a 400/413 was written.
func parseUpload(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAudioUpload+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			detail(w, http.StatusRequestEntityTooLarge, "Audio upload too large")
			return false
		}
		validationError(w, "body -> file: Field required")
		return false
	}
	return true
}

func formFile(r *http.Request, name string) ([]byte, bool, error) {
	if r.MultipartForm == nil {
		return nil, false, nil
	}
	fhs := r.MultipartForm.File[name]
	if len(fhs) == 0 {
		return nil, false, nil
	}
	b, err := readFormFile(fhs[0])
	return b, true, err
}

func readFormFile(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxAudioUpload+1))
}

// --- M3: transcription ---

func (m *Module) handleTranscribe(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	if !parseUpload(w, r) {
		return
	}
	file, ok, err := formFile(r, "file")
	if !ok {
		validationError(w, "body -> file: Field required")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	speakerAudio, _, _ := formFile(r, "speaker_audio")
	convID := r.FormValue("conversation_id") // language/task: accepted and ignored (M13)
	if m.STT == nil {
		detail(w, http.StatusServiceUnavailable, "STT unavailable")
		return
	}
	ctx := r.Context()
	start := m.now()
	scope := &stt.SpeakerScope{HouseholdID: n.HouseholdID, MemberIDs: n.HouseholdMemberIDs}
	tr := newReqTrace()
	// The speaker pass runs inside Transcribe, beside whisper (legacy: one whisper-api call).
	endSTT := tr.measure("stt_transcribe", "whisper", map[string]any{"audio_bytes": len(file), "speaker_audio_bytes": len(speakerAudio)})
	res, err := m.STT.Transcribe(ctx, file, stt.TranscribeOptions{SpeakerAudio: speakerAudio, Speaker: scope})
	endSTT(err)
	trace := Trace{ConversationID: convID, RequestType: "stt", Source: "node", NodeID: n.ID, HouseholdID: n.HouseholdID,
		UserCommand: res.Text, TotalDurationMS: float64(m.now().Sub(start).Microseconds()) / 1000, Spans: tr.spans()}
	if err != nil {
		trace.Status, trace.ErrorMessage = "error", err.Error()
		m.recordTraceAsync(trace)
		m.sttError(w, err)
		return
	}
	m.recordTraceAsync(trace)
	if convID != "" {
		id := turnIdentity{Outcome: res.Speaker.Outcome, Confidence: res.Speaker.Confidence,
			RecognitionOff: res.Speaker.Outcome == stt.OutcomeOff}
		if res.Speaker.UserID != nil && res.Speaker.Outcome == stt.OutcomeMatched {
			id.UserID = *res.Speaker.UserID
		}
		m.signals.recordIdentity(convID, id)
		if len(speakerAudio) > 0 && m.wakeMode(ctx, n.HouseholdID, n.ID) != "off" {
			finish := m.signals.beginWake(convID)
			go m.verifyWake(context.WithoutCancel(ctx), n, speakerAudio, finish)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (m *Module) sttError(w http.ResponseWriter, err error) {
	var ee *stt.EngineError
	switch {
	case errors.Is(err, stt.ErrBadAudio):
		detail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, stt.ErrEngineUnavailable), errors.Is(err, stt.ErrSpeakerUnavailable):
		detail(w, http.StatusServiceUnavailable, err.Error())
	case errors.As(err, &ee):
		httpx.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": ee.Msg, "stderr": ee.Stderr})
	default:
		m.internalError(w, err)
	}
}

// verifyWake transcribes the leading wake slice of the speaker audio and stores the verdict
// (run_wake_verification), after the main transcription, never concurrently with it.
func (m *Module) verifyWake(ctx context.Context, n *nodeCtx, speakerAudio []byte, finish func(*wakeVerdict)) {
	var verdict *wakeVerdict
	defer func() { finish(verdict) }()
	samples, err := stt.DecodeWAV(speakerAudio)
	if err != nil || len(samples) == 0 {
		return
	}
	cut := int(wakeSliceSecs * 16000)
	if cut < len(samples) {
		samples = samples[:cut]
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := m.STT.Transcribe(vctx, stt.EncodeWAV(samples), stt.TranscribeOptions{})
	if err != nil {
		m.deps.Log.Warn("cc: wake verification transcription failed (fail open)", "err", err)
		return
	}
	phrase := m.settings.String(ctx, settingWakePhrase, scopeHN(n.HouseholdID, n.ID))
	verdict = wakeVerdictFor(res.Text, phrase)
	m.deps.Log.Info("cc: wake verification", "node", n.ID, "verdict", verdict.Verdict, "transcript", res.Text)
}

// --- the enrollment handoff ---

// enrollment is one pending node-mic take (V7/V8).
type enrollment struct {
	rid         string
	nodeID      string
	userID      int64
	householdID string
	kind        string // enroll_voice | verify_voice
	lowQuality  bool   // M4 rejected the take (D37); the node reports it as upload_failed
	used        bool   // the take was uploaded (one upload per start)
	seq         int
	expires     time.Time
}

type enrollments struct {
	mu  sync.Mutex
	m   map[string]*enrollment
	seq int
}

func newEnrollments() *enrollments { return &enrollments{m: map[string]*enrollment{}} }

func (e *enrollments) add(x *enrollment, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range e.m {
		if now.After(v.expires) {
			delete(e.m, k)
		}
	}
	e.seq++
	x.seq = e.seq
	e.m[x.rid] = x
}

// claim takes the newest live, unused pending take of kind for (node, user).
func (e *enrollments) claim(nodeID string, userID int64, kind string, now time.Time) *enrollment {
	e.mu.Lock()
	defer e.mu.Unlock()
	var best *enrollment
	for _, v := range e.m {
		if v.nodeID == nodeID && v.userID == userID && v.kind == kind && !v.used && !now.After(v.expires) {
			if best == nil || v.seq > best.seq {
				best = v
			}
		}
	}
	if best != nil {
		best.used = true
	}
	return best
}

func (e *enrollments) get(rid string) *enrollment {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.m[rid]
}

func (e *enrollments) markLowQuality(x *enrollment) {
	e.mu.Lock()
	x.lowQuality = true
	e.mu.Unlock()
}

func queryUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := r.URL.Query().Get("user_id")
	if v == "" {
		validationError(w, "query -> user_id: Field required")
		return 0, false
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		validationError(w, "query -> user_id: Input should be a valid integer, unable to parse string as an integer")
		return 0, false
	}
	return id, true
}

// handleEnroll is M4. The take must belong to a pending start for this node and user, and is
// stored in the node's household. A take failing the D37 gate is 422 low_quality, and the
// node's resulting V9 failure is relayed to mobile as error "low_quality" (stt Q1).
func (m *Module) handleEnroll(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	userID, ok := queryUserID(w, r)
	if !ok || !parseUpload(w, r) {
		return
	}
	file, has, err := formFile(r, "file")
	if !has {
		validationError(w, "body -> file: Field required")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if m.STT == nil {
		detail(w, http.StatusServiceUnavailable, "STT unavailable")
		return
	}
	p := m.enroll.claim(n.ID, userID, "enroll_voice", m.now())
	if p == nil {
		detail(w, http.StatusForbidden, "No pending voice enrollment for this node and user")
		return
	}
	res, err := m.STT.Enroll(r.Context(), p.householdID, userID, file, nil)
	var lq *stt.LowQualityError
	if errors.As(err, &lq) {
		m.enroll.markLowQuality(p)
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"success": false, "error": "low_quality", "reason": lq.Reason, "speech_seconds": lq.SpeechSeconds, "detail": lq.Error(),
		})
		return
	}
	if err != nil {
		m.sttError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// handleVerify is M5. household_id is required for wire compatibility but the node's own
// household is used (D4: legacy took it from the query).
func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	userID, ok := queryUserID(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("household_id") == "" {
		validationError(w, "query -> household_id: Field required")
		return
	}
	if !parseUpload(w, r) {
		return
	}
	file, has, err := formFile(r, "file")
	if !has {
		validationError(w, "body -> file: Field required")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if m.STT == nil {
		detail(w, http.StatusServiceUnavailable, "STT unavailable")
		return
	}
	p := m.enroll.claim(n.ID, userID, "verify_voice", m.now())
	if p == nil {
		detail(w, http.StatusForbidden, "No pending voice verification for this node and user")
		return
	}
	res, err := m.STT.Verify(r.Context(), p.householdID, userID, file)
	if errors.Is(err, stt.ErrNoProfile) {
		detail(w, http.StatusNotFound, "No voice profile enrolled")
		return
	}
	if err != nil {
		m.sttError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// handleStartNodeVoice is V7 (enroll_voice) and V8 (verify_voice).
func (m *Module) handleStartNodeVoice(kind string, defaultSecs float64) userHandler {
	return func(w http.ResponseWriter, r *http.Request, u authn.User) {
		b, _, ok := readBody(w, r, false)
		if !ok {
			return
		}
		nodeID, _ := b.str("node_id", true)
		prompt, _ := b.optStrPtr("prompt_text")
		secs, hasSecs := b.number("duration_secs")
		if !b.done(w) {
			return
		}
		ctx := r.Context()
		row, err := m.nodeByID(ctx, nodeID)
		if err != nil {
			detail(w, http.StatusNotFound, "Node not found")
			return
		}
		if !row.online(m.now()) {
			detail(w, http.StatusConflict, "Node is offline")
			return
		}
		hh := ""
		if row.householdID.Valid {
			hh = row.householdID.String
			if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
				m.writeErr(w, err)
				return
			}
		}
		if !hasSecs || secs == 0 {
			secs = defaultSecs
		}
		pt := ""
		if prompt != nil {
			pt = *prompt
		}
		rid := uuid4()
		m.enroll.add(&enrollment{rid: rid, nodeID: nodeID, userID: u.ID, householdID: hh, kind: kind,
			expires: m.now().Add(enrollmentTTL)}, m.now())
		m.bus.ExpectFor(rid, nodeID, enrollResultTTL)
		var hhv any
		if hh != "" {
			hhv = hh
		}
		m.bus.CommandWithID(nodeID, kind, map[string]any{
			"user_id": u.ID, "household_id": hhv, "prompt_text": pt, "duration_secs": secs,
		}, rid)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"request_id": rid})
	}
}

// handleVoiceProfileResult is V9: the node's take result, through the shared sink. A take
// M4 rejected as low quality arrives as the node's generic upload failure and is relayed as
// error "low_quality", which mobile already displays (stt Q1).
func (m *Module) handleVoiceProfileResult(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	raw, ok := readRawObject(w, r)
	if !ok {
		return
	}
	rid := r.PathValue("request_id")
	if p := m.enroll.get(rid); p != nil && p.lowQuality {
		var res map[string]any
		if json.Unmarshal(raw, &res) == nil && res["success"] == false {
			if e, _ := res["error"].(string); strings.HasPrefix(e, "upload_failed") {
				res["error"] = "low_quality"
				raw, _ = json.Marshal(res)
			}
		}
	}
	if m.deliver(rid, n.ID, raw) == WrongNode {
		detail(w, http.StatusForbidden, "Request does not belong to this node")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handlePollVoiceProfileResult is V10: 202 {"detail":"pending"} until the node posts, then the
// result once (consumed). Only the user who started the take may poll it.
func (m *Module) handlePollVoiceProfileResult(w http.ResponseWriter, r *http.Request, u authn.User) {
	rid := r.PathValue("request_id")
	if p := m.enroll.get(rid); p != nil && p.userID != u.ID {
		detail(w, http.StatusForbidden, "Not authorized")
		return
	}
	res, ok := m.bus.Take(rid)
	if !ok {
		detail(w, http.StatusAccepted, "pending")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res)
}

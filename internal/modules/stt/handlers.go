package stt

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// --- FastAPI-shaped request parsing ---

// form collects pydantic-style validation errors in FastAPI's order (path, query, body).
type form struct {
	r    *http.Request
	errs []httpx.FieldError
}

func (f *form) fail(typ, msg string, input any, loc ...any) {
	f.errs = append(f.errs, httpx.FieldError{Type: typ, Loc: loc, Msg: msg, Input: input})
}

func (f *form) query(name string) (string, bool) {
	q := f.r.URL.Query()
	if !q.Has(name) {
		return "", false
	}
	return q.Get(name), true
}

func (f *form) parseInt(where, name, raw string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		f.fail("int_parsing", "Input should be a valid integer, unable to parse string as an integer", raw, where, name)
		return 0, false
	}
	return v, true
}

func (f *form) pathInt(name string) int64 {
	v, _ := f.parseInt("path", name, f.r.PathValue(name))
	return v
}

func (f *form) requiredInt(name string) int64 {
	raw, ok := f.query(name)
	if !ok {
		f.fail("missing", "Field required", nil, "query", name)
		return 0
	}
	v, _ := f.parseInt("query", name, raw)
	return v
}

func (f *form) optionalInt(name string) *int64 {
	raw, ok := f.query(name)
	if !ok {
		return nil
	}
	v, ok := f.parseInt("query", name, raw)
	if !ok {
		return nil
	}
	return &v
}

func (f *form) requiredStr(name string) string {
	raw, ok := f.query(name)
	if !ok {
		f.fail("missing", "Field required", nil, "query", name)
	}
	return raw
}

// optionalFloat parses a query float with pydantic's ge/le bounds.
func (f *form) optionalFloat(name string, lo, hi float64) *float64 {
	raw, ok := f.query(name)
	if !ok {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	switch {
	case err != nil:
		f.fail("float_parsing", "Input should be a valid number, unable to parse string as a number", raw, "query", name)
	case v < lo:
		f.fail("greater_than_equal", fmt.Sprintf("Input should be greater than or equal to %g", lo), raw, "query", name)
	case v > hi:
		f.fail("less_than_equal", fmt.Sprintf("Input should be less than or equal to %g", hi), raw, "query", name)
	default:
		return &v
	}
	return nil
}

func (f *form) optionalBool(name string, def bool) bool {
	raw, ok := f.query(name)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	}
	f.fail("bool_parsing", "Input should be a valid boolean, unable to interpret input", raw, "query", name)
	return def
}

// files parses the multipart body. A body that isn't multipart simply has no files, which
// the required-file check turns into FastAPI's 422.
func (f *form) parseBody(w http.ResponseWriter, max int64) (tooBig bool) {
	if cl := f.r.ContentLength; cl > 2*max+(1<<20) {
		return true
	}
	f.r.Body = http.MaxBytesReader(w, f.r.Body, 2*max+(1<<20))
	if err := f.r.ParseMultipartForm(32 << 20); err != nil {
		var mbe *http.MaxBytesError
		return errors.As(err, &mbe)
	}
	return false
}

func (f *form) file(name string, required bool) *multipart.FileHeader {
	if f.r.MultipartForm != nil {
		if fhs := f.r.MultipartForm.File[name]; len(fhs) > 0 {
			return fhs[0]
		}
	}
	if required {
		f.fail("missing", "Field required", nil, "body", name)
	}
	return nil
}

func (f *form) done(w http.ResponseWriter) bool {
	if len(f.errs) > 0 {
		httpx.ValidationError(w, f.errs...)
		return false
	}
	return true
}

var errTooLarge = errors.New("Audio upload too large")

func readFile(fh *multipart.FileHeader, max int64) ([]byte, error) {
	if fh.Size > max {
		return nil, errTooLarge
	}
	file, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errTooLarge
	}
	return b, nil
}

// parseMemberIDs is the legacy parse_household_member_ids: a comma list; any bad entry
// means no members at all.
func parseMemberIDs(h string) []int64 {
	out := []int64{}
	for _, p := range strings.Split(h, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return []int64{}
		}
		out = append(out, v)
	}
	return out
}

// --- error mapping ---

func (m *Module) writeErr(w http.ResponseWriter, err error) {
	var lq *LowQualityError
	var ee *EngineError
	switch {
	case errors.As(err, &lq):
		// D37: the legacy node result shape, so command-center can relay it unchanged.
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"success": false, "error": "low_quality", "reason": lq.Reason,
			"speech_seconds": lq.SpeechSeconds, "detail": lq.Error(),
		})
	case errors.Is(err, ErrBadAudio):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrSampleIndex):
		httpx.Error(w, http.StatusBadRequest, ErrSampleIndex.Error())
	case errors.Is(err, ErrSpeakerUnavailable):
		httpx.Error(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, ErrEngineUnavailable):
		httpx.Error(w, http.StatusServiceUnavailable, err.Error())
	case errors.As(err, &ee):
		m.deps.Log.Error("stt: transcription failed", "err", ee.Msg)
		httpx.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": ee.Msg, "stderr": ee.Stderr})
	default:
		m.deps.Log.Error("stt: internal error", "err", err)
		httpx.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

// --- POST /transcribe ---

func (m *Module) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	max := m.maxUpload()
	if r.ContentLength > max {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Audio upload too large")
		return
	}
	f := &form{r: r}
	prompt, _ := f.query("prompt")
	preprocess := f.optionalBool("preprocess", false)
	temp := f.optionalFloat("temperature", 0, 1)
	tempInc := f.optionalFloat("temperature_inc", 0, 1)
	beam := f.optionalInt("beam_size")
	if beam != nil && (*beam < 1 || *beam > 16) {
		bound, typ := "greater than or equal to 1", "greater_than_equal"
		if *beam > 16 {
			bound, typ = "less than or equal to 16", "less_than_equal"
		}
		f.fail(typ, "Input should be "+bound, strconv.FormatInt(*beam, 10), "query", "beam_size")
	}
	speakerRecognition := f.optionalBool("speaker_recognition", true)
	if f.parseBody(w, max) {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Audio upload too large")
		return
	}
	fileH := f.file("file", true)
	speakerH := f.file("speaker_audio", false)
	if !f.done(w) {
		return
	}
	opts := m.transcribeOpts(r, prompt, preprocess, temp, tempInc, beam, speakerRecognition)
	audio, err := readFile(fileH, max)
	if err == nil && speakerH != nil {
		opts.SpeakerAudio, err = readFile(speakerH, max)
	}
	if errors.Is(err, errTooLarge) {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Audio upload too large")
		return
	}
	if err != nil {
		m.writeErr(w, err)
		return
	}
	res, err := m.Transcribe(r.Context(), audio, opts)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (m *Module) transcribeOpts(r *http.Request, prompt string, preprocess bool, temp, tempInc *float64, beam *int64, speaker bool) TranscribeOptions {
	opts := TranscribeOptions{Prompt: prompt, Preprocess: preprocess, Temperature: temp, TemperatureInc: tempInc}
	if beam != nil {
		opts.BeamSize = int(*beam)
	}
	if speaker {
		// The household context command-center's media proxy sends (legacy RequestContext).
		// The speaker search is limited to the listed members; none listed means none scored.
		opts.Speaker = &SpeakerScope{
			HouseholdID: r.Header.Get("X-Context-Household-Id"),
			MemberIDs:   parseMemberIDs(r.Header.Get("X-Context-Household-Member-Ids")),
		}
	}
	return opts
}

// --- voice profiles ---

func (m *Module) handleEnroll(w http.ResponseWriter, r *http.Request) {
	f := &form{r: r}
	uid := f.requiredInt("user_id")
	hh := f.requiredStr("household_id")
	idx := f.optionalInt("sample_index")
	if f.parseBody(w, m.maxUpload()) {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Audio upload too large")
		return
	}
	fh := f.file("file", true)
	if !f.done(w) {
		return
	}
	var sampleIndex *int
	if idx != nil {
		if *idx < 0 || *idx > 999 {
			httpx.Error(w, http.StatusBadRequest, ErrSampleIndex.Error())
			return
		}
		i := int(*idx)
		sampleIndex = &i
	}
	wav, err := readFile(fh, m.maxUpload())
	if errors.Is(err, errTooLarge) {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Audio upload too large")
		return
	}
	if err != nil {
		m.writeErr(w, err)
		return
	}
	res, err := m.Enroll(r.Context(), hh, uid, wav, sampleIndex)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request) {
	f := &form{r: r}
	uid := f.requiredInt("user_id")
	hh := f.requiredStr("household_id")
	if f.parseBody(w, m.maxUpload()) {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Audio upload too large")
		return
	}
	fh := f.file("file", true)
	if !f.done(w) {
		return
	}
	wav, err := readFile(fh, m.maxUpload())
	if errors.Is(err, errTooLarge) {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "Audio upload too large")
		return
	}
	if err != nil {
		m.writeErr(w, err)
		return
	}
	res, err := m.Verify(r.Context(), hh, uid, wav)
	if errors.Is(err, ErrNoProfile) {
		httpx.Error(w, http.StatusNotFound, fmt.Sprintf("No voice profile enrolled for user %d", uid))
		return
	}
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (m *Module) handleCheck(w http.ResponseWriter, r *http.Request) {
	f := &form{r: r}
	uid := f.requiredInt("user_id")
	hh := f.requiredStr("household_id")
	if !f.done(w) {
		return
	}
	n, err := m.sampleCount(r.Context(), hh, uid)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"exists": n > 0, "user_id": uid, "sample_count": n})
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	f := &form{r: r}
	hh := f.requiredStr("household_id")
	if !f.done(w) {
		return
	}
	profiles, err := m.Profiles(r.Context(), hh)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"household_id": hh, "profiles": profiles})
}

func (m *Module) handleSamples(w http.ResponseWriter, r *http.Request) {
	f := &form{r: r}
	uid := f.pathInt("user_id")
	hh := f.requiredStr("household_id")
	if !f.done(w) {
		return
	}
	samples, err := m.Samples(r.Context(), hh, uid)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"household_id": hh, "user_id": uid, "samples": samples})
}

func (m *Module) handleDeleteSample(w http.ResponseWriter, r *http.Request) {
	f := &form{r: r}
	uid := f.pathInt("user_id")
	idx := f.pathInt("sample_index")
	hh := f.requiredStr("household_id")
	if !f.done(w) {
		return
	}
	remaining, err := m.DeleteSample(r.Context(), hh, uid, int(idx))
	if errors.Is(err, ErrSampleNotFound) {
		httpx.Error(w, http.StatusNotFound, fmt.Sprintf("Sample %d not found for user %d", idx, uid))
		return
	}
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "deleted", "user_id": uid, "sample_index": idx, "remaining_samples": remaining,
	})
}

func (m *Module) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	f := &form{r: r}
	uid := f.pathInt("user_id")
	hh := f.requiredStr("household_id")
	if !f.done(w) {
		return
	}
	err := m.DeleteProfile(r.Context(), hh, uid)
	if errors.Is(err, ErrNoProfile) {
		httpx.Error(w, http.StatusNotFound, "Voice profile not found")
		return
	}
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "deleted", "user_id": uid, "household_id": hh})
}

// handlePurge is the account purge legacy command-center called (api/me.py); jarvisd's auth
// calls PurgeUser in-process instead (D20). Idempotent.
func (m *Module) handlePurge(w http.ResponseWriter, r *http.Request) {
	f := &form{r: r}
	uid := f.pathInt("user_id")
	if !f.done(w) {
		return
	}
	households, err := m.purgeUserHouseholds(r.Context(), uid)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.deps.Log.Info("stt: deleted all voice profiles", "user_id", uid, "households", households)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "deleted", "user_id": uid, "households": households})
}

package stt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/alexberardi/jarvis-server/internal/voice/sherpa"
)

// SpeakerModel is a loaded speaker-embedding model (ERes2Net via sherpa-onnx in jarvisd).
type SpeakerModel interface {
	// ID tags every voiceprint the model produces (D34): a voiceprint made by another model is
	// never scored.
	ID() string
	// Embed returns the raw embedding of mono samples in [-1, 1].
	Embed(samples []float32, sampleRate int) ([]float32, error)
}

// Speaker-side errors. The HTTP layer maps them to the legacy statuses; in-process callers
// (command-center) match them with errors.Is / errors.As.
var (
	// ErrRecognitionOff: voice.recognition_enabled is off for the household (D35). Callers say
	// "speaker recognition is off", not "I'm not sure who's speaking" (M14).
	ErrRecognitionOff = errors.New("stt: speaker recognition is off")
	// ErrSpeakerUnavailable: no speaker model is installed or it failed to load.
	ErrSpeakerUnavailable = errors.New("stt: speaker model unavailable")
	ErrNoProfile          = errors.New("stt: no voice profile")
	ErrSampleNotFound     = errors.New("stt: sample not found")
	ErrSampleIndex        = errors.New("sample_index must be in [0, 999]")
)

// LowQualityError is the D37 enrollment gate: the take is not stored. Its wire form is the
// legacy node result shape, {"success": false, "error": "low_quality"}.
type LowQualityError struct {
	Reason        string  // "too_little_speech", "inconsistent", "too_short"
	SpeechSeconds float64 // VAD speech found in the take
	MinSpeech     float64 // voice.enroll_min_speech_seconds
	Score         float64 // for "inconsistent": the take's cosine to the user's other takes
}

func (e *LowQualityError) Error() string {
	switch e.Reason {
	case "inconsistent":
		return fmt.Sprintf("low_quality: take does not match your other samples (score %.3f)", e.Score)
	case "too_short":
		return "low_quality: not enough audio for a voiceprint"
	}
	return fmt.Sprintf("low_quality: %.1f s of speech, need at least %.1f s", e.SpeechSeconds, e.MinSpeech)
}

// Outcomes of the speaker pass (Speaker.Outcome). Only OutcomeMatched carries a user id.
const (
	OutcomeMatched    = "matched"
	OutcomeNoMatch    = "no_match"    // best score at or below voice.similarity_threshold
	OutcomeAmbiguous  = "ambiguous"   // above threshold but within voice.min_speaker_margin of the runner-up (D21: unknown)
	OutcomeNoProfiles = "no_profiles" // nobody in scope has a voiceprint for the active model
	OutcomeOff        = "off"         // recognition disabled (D35)
	OutcomeSkipped    = "skipped"     // caller passed speaker_recognition=false
	OutcomeError      = "error"       // model missing or the clip could not be embedded
)

// Speaker is the transcribe response's `speaker`. Abstain keeps the best score in Confidence
// with a null UserID (invariant 6): consumers key on UserID, never on confidence.
type Speaker struct {
	UserID     *int64  `json:"user_id"`
	Confidence float64 `json:"confidence"`
	// Outcome says why UserID is what it is (not on the wire).
	Outcome string `json:"-"`
}

// SpeakerScope is who a clip may be attributed to.
type SpeakerScope struct {
	HouseholdID string
	// MemberIDs limits the search to these users (invariant 1: the node's validated household
	// members). Empty means nobody, as in the legacy service, unless AllMembers is set.
	MemberIDs []int64
	// AllMembers searches every voiceprint enrolled in the household.
	AllMembers bool
}

// --- the speaker model ---

type sherpaSpeaker struct {
	ex *sherpa.SpeakerExtractor
	id string
}

func (s *sherpaSpeaker) ID() string { return s.id }
func (s *sherpaSpeaker) Embed(samples []float32, rate int) ([]float32, error) {
	return s.ex.Embed(samples, rate)
}

type loadedModel struct {
	path string
	m    SpeakerModel
}

// speakerModel returns the active speaker model, loading (or swapping in) the one the model
// manager points at. Replaced extractors are closed only when the module stops: a request may
// still hold the old one.
func (m *Module) speakerModel(ctx context.Context) (SpeakerModel, error) {
	if m.Speaker != nil {
		return m.Speaker, nil
	}
	if m.Models == nil {
		return nil, ErrSpeakerUnavailable
	}
	path, ok := m.Models.ModelPath(ctx, "speaker")
	if !ok || path == "" {
		return nil, fmt.Errorf("%w: no speaker model installed", ErrSpeakerUnavailable)
	}
	m.modelMu.Lock()
	defer m.modelMu.Unlock()
	if m.model != nil && m.model.path == path {
		return m.model.m, nil
	}
	if err := sherpa.Load(m.libDir()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpeakerUnavailable, err)
	}
	id, err := modelID(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpeakerUnavailable, err)
	}
	ex, err := sherpa.NewSpeakerExtractor(sherpa.SpeakerConfig{Model: path, NumThreads: 2})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSpeakerUnavailable, err)
	}
	if m.model != nil {
		m.retired = append(m.retired, m.model.m)
	}
	m.model = &loadedModel{path: path, m: &sherpaSpeaker{ex: ex, id: id}}
	m.deps.Log.Info("stt: speaker model loaded", "path", path, "model_id", id, "dim", ex.Dim())
	return m.model.m, nil
}

// activeModelID is the id voiceprints must carry to count, without loading the native model.
// "" when no model is configured: then nothing is filtered (and nothing can be scored).
func (m *Module) activeModelID(ctx context.Context) string {
	if m.Speaker != nil {
		return m.Speaker.ID()
	}
	if m.Models == nil {
		return ""
	}
	path, ok := m.Models.ModelPath(ctx, "speaker")
	if !ok || path == "" {
		return ""
	}
	id, err := modelID(path)
	if err != nil {
		return ""
	}
	return id
}

func (m *Module) closeModels() {
	m.modelMu.Lock()
	defer m.modelMu.Unlock()
	all := m.retired
	if m.model != nil {
		all = append(all, m.model.m)
	}
	for _, x := range all {
		if s, ok := x.(*sherpaSpeaker); ok {
			s.ex.Close()
		}
	}
	m.model, m.retired = nil, nil
}

var idCache sync.Map // path → idEntry

type idEntry struct {
	size  int64
	mtime time.Time
	id    string
}

// modelID names a model file by content: "<basename>@<sha256[:12]>". Re-installing the same
// file elsewhere keeps the voiceprints; a different model (even under the same name) does not.
func modelID(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if v, ok := idCache.Load(path); ok {
		if e := v.(idEntry); e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
			return e.id, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	id := base + "@" + hex.EncodeToString(h.Sum(nil))[:12]
	idCache.Store(path, idEntry{size: st.Size(), mtime: st.ModTime(), id: id})
	return id, nil
}

// embed returns the unit embedding of a 16 kHz clip.
func embedClip(sm SpeakerModel, samples []float32) ([]float32, error) {
	v, err := sm.Embed(samples, SampleRate)
	if err != nil {
		return nil, err
	}
	if len(v) == 0 {
		return nil, sherpa.ErrTooShort
	}
	return normalize(v), nil
}

// --- the voiceprint store ---

type voiceprint struct {
	UserID int64
	Index  int
	Vec    []float32
	Bytes  int
}

// voiceprints loads the household's voiceprints for modelID ("" = any model), optionally
// limited to users.
func (m *Module) voiceprints(ctx context.Context, householdID, modelID string, users []int64, all bool) ([]voiceprint, error) {
	q := `SELECT user_id, sample_index, dim, embedding FROM stt_voiceprints WHERE household_id = ?`
	args := []any{householdID}
	if modelID != "" {
		q += ` AND model_id = ?`
		args = append(args, modelID)
	}
	if !all {
		if len(users) == 0 {
			return nil, nil
		}
		q += ` AND user_id IN (?` + strings.Repeat(",?", len(users)-1) + `)`
		for _, u := range users {
			args = append(args, u)
		}
	}
	q += ` ORDER BY user_id, sample_index`
	rows, err := m.deps.DB.Read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []voiceprint
	for rows.Next() {
		var vp voiceprint
		var dim int
		var blob []byte
		if err := rows.Scan(&vp.UserID, &vp.Index, &dim, &blob); err != nil {
			return nil, err
		}
		vp.Bytes = len(blob)
		if vp.Vec, err = decodeEmbedding(blob, dim); err != nil {
			return nil, err
		}
		out = append(out, vp)
	}
	return out, rows.Err()
}

// centroids groups voiceprints into one unit centroid per user.
func centroids(vps []voiceprint) map[int64][]float32 {
	by := map[int64][][]float32{}
	for _, vp := range vps {
		by[vp.UserID] = append(by[vp.UserID], vp.Vec)
	}
	out := make(map[int64][]float32, len(by))
	for u, vs := range by {
		out[u] = centroid(vs)
	}
	return out
}

// --- identification ---

func (m *Module) threshold(ctx context.Context, householdID string) float64 {
	return m.settings.Float(ctx, "voice.similarity_threshold", settings.Scope{HouseholdID: householdID})
}

// RecognitionEnabled reports voice.recognition_enabled for a household (D35: off by default).
func (m *Module) RecognitionEnabled(ctx context.Context, householdID string) bool {
	return m.settings.Bool(ctx, "voice.recognition_enabled", settings.Scope{HouseholdID: householdID})
}

// Identify scores 16 kHz mono samples against the voiceprints in scope: one similarity
// threshold plus the margin gate (D33). It returns ErrRecognitionOff when the household has
// recognition disabled (D35); every other failure is an Outcome, not an error, because a
// speaker pass must never fail the transcription it rides on.
func (m *Module) Identify(ctx context.Context, scope SpeakerScope, samples []float32) (Speaker, error) {
	if !m.RecognitionEnabled(ctx, scope.HouseholdID) {
		return Speaker{Outcome: OutcomeOff}, ErrRecognitionOff
	}
	return m.identify(ctx, scope, samples), nil
}

func (m *Module) identify(ctx context.Context, scope SpeakerScope, samples []float32) Speaker {
	log := m.deps.Log
	sm, err := m.speakerModel(ctx)
	if err != nil {
		log.Warn("stt: speaker pass skipped", "err", err)
		return Speaker{Outcome: OutcomeError}
	}
	vps, err := m.voiceprints(ctx, scope.HouseholdID, sm.ID(), scope.MemberIDs, scope.AllMembers)
	if err != nil {
		log.Error("stt: load voiceprints", "err", err)
		return Speaker{Outcome: OutcomeError}
	}
	profiles := centroids(vps)
	if len(profiles) == 0 {
		log.Warn("stt: speaker_profiles_empty — no voiceprints in scope (check enrollment and the member scope)",
			"household", scope.HouseholdID, "members", scope.MemberIDs, "all", scope.AllMembers)
		return Speaker{Outcome: OutcomeNoProfiles}
	}
	q, err := embedClip(sm, samples)
	if err != nil {
		log.Warn("stt: speaker embedding failed", "err", err)
		return Speaker{Outcome: OutcomeError}
	}
	type sc struct {
		uid   int64
		score float64
	}
	scores := make([]sc, 0, len(profiles))
	for uid, c := range profiles {
		scores = append(scores, sc{uid, dot(q, c)})
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}
		return scores[i].uid < scores[j].uid
	})
	best := scores[0]
	second := 0.0
	if len(scores) > 1 {
		second = scores[1].score
	}
	margin := best.score - second
	thr := m.threshold(ctx, scope.HouseholdID)
	minMargin := m.settings.Float(ctx, "voice.min_speaker_margin", settings.Scope{HouseholdID: scope.HouseholdID})
	ambiguous := len(scores) >= 2 && margin < minMargin
	out := Speaker{Confidence: best.score}
	switch {
	case best.score > thr && !ambiguous:
		uid := best.uid
		out.UserID, out.Outcome = &uid, OutcomeMatched
	case best.score > thr:
		out.Outcome = OutcomeAmbiguous
	default:
		out.Outcome = OutcomeNoMatch
	}
	var parts []string
	for _, s := range scores {
		parts = append(parts, fmt.Sprintf("%d:%.3f", s.uid, s.score))
	}
	log.Info("stt: speaker match", "household", scope.HouseholdID, "best_user", best.uid,
		"score", round(best.score, 3), "second", round(second, 3), "margin", round(margin, 3),
		"threshold", thr, "min_margin", minMargin, "duration_s", round(float64(len(samples))/SampleRate, 2),
		"model", sm.ID(), "members", len(scores), "scores", strings.Join(parts, ","), "outcome", out.Outcome)
	return out
}

// --- enrollment ---

// EnrollResult is the enroll response (M4).
type EnrollResult struct {
	Status       string `json:"status"`
	UserID       int64  `json:"user_id"`
	HouseholdID  string `json:"household_id"`
	SampleIndex  int    `json:"sample_index"`
	TotalSamples int    `json:"total_samples"`
}

// Enroll embeds one take and stores it as the user's voiceprint sample in the household
// (D34: the audio is discarded). sampleIndex nil takes the next free index; an explicit one
// overwrites that slot. Takes failing the D37 gate return *LowQualityError and store nothing.
func (m *Module) Enroll(ctx context.Context, householdID string, userID int64, wav []byte, sampleIndex *int) (EnrollResult, error) {
	samples, err := DecodeWAV(wav)
	if err != nil {
		return EnrollResult{}, err
	}
	return m.enrollSamples(ctx, householdID, userID, samples, sampleIndex)
}

func (m *Module) enrollSamples(ctx context.Context, householdID string, userID int64, samples []float32, sampleIndex *int) (EnrollResult, error) {
	sm, err := m.speakerModel(ctx)
	if err != nil {
		return EnrollResult{}, err
	}
	modelID := sm.ID()
	existing, err := m.voiceprints(ctx, householdID, modelID, []int64{userID}, false)
	if err != nil {
		return EnrollResult{}, err
	}
	idx := 0
	if sampleIndex != nil {
		idx = *sampleIndex
	} else if len(existing) > 0 {
		idx = existing[len(existing)-1].Index + 1
	}
	if idx < 0 || idx > 999 {
		return EnrollResult{}, ErrSampleIndex
	}

	// D37: enough speech, and not wildly unlike the user's other takes.
	minSpeech := m.settings.Float(ctx, "voice.enroll_min_speech_seconds", settings.Scope{HouseholdID: householdID})
	speech := SpeechSeconds(samples)
	if speech+float64(vadFrameMs)/2000 < minSpeech {
		return EnrollResult{}, &LowQualityError{Reason: "too_little_speech", SpeechSeconds: round(speech, 2), MinSpeech: minSpeech}
	}
	vec, err := embedClip(sm, samples)
	if err != nil {
		if errors.Is(err, sherpa.ErrTooShort) {
			return EnrollResult{}, &LowQualityError{Reason: "too_short", SpeechSeconds: round(speech, 2)}
		}
		return EnrollResult{}, fmt.Errorf("%w: %v", ErrSpeakerUnavailable, err)
	}
	var others [][]float32
	for _, vp := range existing {
		if vp.Index != idx {
			others = append(others, vp.Vec)
		}
	}
	if len(others) > 0 {
		floor := m.settings.Float(ctx, "voice.enroll_min_consistency", settings.Scope{HouseholdID: householdID})
		if s := dot(vec, centroid(others)); s < floor {
			return EnrollResult{}, &LowQualityError{Reason: "inconsistent", SpeechSeconds: round(speech, 2), Score: round(s, 4)}
		}
	}

	total := 0
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		// D34: voiceprints from another model can never be scored again; the first take under
		// the new model clears them.
		if _, err := tx.ExecContext(ctx, `DELETE FROM stt_voiceprints WHERE household_id = ? AND user_id = ? AND model_id <> ?`,
			householdID, userID, modelID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO stt_voiceprints
			(household_id, user_id, sample_index, model_id, dim, embedding, speech_ms, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			householdID, userID, idx, modelID, len(vec), encodeEmbedding(vec), int(speech*1000),
			m.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM stt_voiceprints WHERE household_id = ? AND user_id = ?`,
			householdID, userID).Scan(&total)
	})
	if err != nil {
		return EnrollResult{}, err
	}
	m.deps.Log.Info("stt: enrolled voiceprint sample", "user_id", userID, "household", householdID,
		"index", idx, "speech_s", round(speech, 2), "model", modelID)
	return EnrollResult{Status: "enrolled", UserID: userID, HouseholdID: householdID, SampleIndex: idx, TotalSamples: total}, nil
}

// VerifyResult is the verify response (M5): confidence rounded to 4 dp.
type VerifyResult struct {
	Matched    bool    `json:"matched"`
	Confidence float64 `json:"confidence"`
	UserID     int64   `json:"user_id"`
}

// Verify scores one take against the user's own voiceprint with voice.similarity_threshold
// (D33: the legacy fixed 0.45 is gone). It is not gated on voice.recognition_enabled: it is
// the enrollment wizard's self-test. ErrNoProfile when the user has no voiceprint.
func (m *Module) Verify(ctx context.Context, householdID string, userID int64, wav []byte) (VerifyResult, error) {
	n, err := m.sampleCount(ctx, householdID, userID)
	if err != nil {
		return VerifyResult{}, err
	}
	if n == 0 {
		return VerifyResult{}, ErrNoProfile
	}
	samples, err := DecodeWAV(wav)
	if err != nil {
		return VerifyResult{}, err
	}
	if _, err := m.speakerModel(ctx); err != nil {
		return VerifyResult{}, err
	}
	sp := m.identify(ctx, SpeakerScope{HouseholdID: householdID, MemberIDs: []int64{userID}}, samples)
	m.deps.Log.Info("stt: voice profile verify", "user_id", userID, "matched", sp.UserID != nil, "confidence", round(sp.Confidence, 3))
	return VerifyResult{Matched: sp.UserID != nil, Confidence: round(sp.Confidence, 4), UserID: userID}, nil
}

// --- profile management ---

// Sample is one enrolled take. Filename and SizeBytes keep the legacy shape: there is no WAV
// any more (D34), so the size is the stored voiceprint's.
type Sample struct {
	Index     int    `json:"index"`
	Filename  string `json:"filename"`
	SizeBytes int    `json:"size_bytes"`
}

func sampleFilename(i int) string { return fmt.Sprintf("sample_%03d.wav", i) }

// Samples lists a user's takes in the household (active model only, when one is configured).
func (m *Module) Samples(ctx context.Context, householdID string, userID int64) ([]Sample, error) {
	vps, err := m.voiceprints(ctx, householdID, m.activeModelID(ctx), []int64{userID}, false)
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(vps))
	for _, vp := range vps {
		out = append(out, Sample{Index: vp.Index, Filename: sampleFilename(vp.Index), SizeBytes: vp.Bytes})
	}
	return out, nil
}

func (m *Module) sampleCount(ctx context.Context, householdID string, userID int64) (int, error) {
	s, err := m.Samples(ctx, householdID, userID)
	return len(s), err
}

// ProfileSummary is one entry of the household listing: users are named by a one-way hash.
type ProfileSummary struct {
	Filename string `json:"filename"`
	Samples  int    `json:"samples"`
}

// HashUserID is the legacy hash_user_id: sha256(str(user_id))[:16].
func HashUserID(userID int64) string {
	h := sha256.Sum256([]byte(strconv.FormatInt(userID, 10)))
	return hex.EncodeToString(h[:])[:16]
}

// Profiles lists the household's enrolled users (by hash) and their sample counts.
func (m *Module) Profiles(ctx context.Context, householdID string) ([]ProfileSummary, error) {
	vps, err := m.voiceprints(ctx, householdID, m.activeModelID(ctx), nil, true)
	if err != nil {
		return nil, err
	}
	counts := map[int64]int{}
	var users []int64
	for _, vp := range vps {
		if counts[vp.UserID] == 0 {
			users = append(users, vp.UserID)
		}
		counts[vp.UserID]++
	}
	out := make([]ProfileSummary, 0, len(users))
	for _, u := range users {
		out = append(out, ProfileSummary{Filename: HashUserID(u), Samples: counts[u]})
	}
	slices.SortFunc(out, func(a, b ProfileSummary) int { return strings.Compare(a.Filename, b.Filename) })
	return out, nil
}

// DeleteSample removes one take. ErrSampleNotFound when there is none at that index.
func (m *Module) DeleteSample(ctx context.Context, householdID string, userID int64, index int) (remaining int, err error) {
	res, err := m.deps.DB.Write.ExecContext(ctx,
		`DELETE FROM stt_voiceprints WHERE household_id = ? AND user_id = ? AND sample_index = ?`, householdID, userID, index)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrSampleNotFound
	}
	return m.sampleCount(ctx, householdID, userID)
}

// DeleteProfile removes the user's voiceprint in one household. ErrNoProfile when none.
func (m *Module) DeleteProfile(ctx context.Context, householdID string, userID int64) error {
	res, err := m.deps.DB.Write.ExecContext(ctx,
		`DELETE FROM stt_voiceprints WHERE household_id = ? AND user_id = ?`, householdID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoProfile
	}
	m.deps.Log.Info("stt: deleted voice profile", "user_id", userID, "household", householdID)
	return nil
}

// PurgeUser hard-deletes the user's voiceprints in every household, inside tx (D20). It has
// the auth module's UserDeletedHook signature: register it with auth.OnUserDeleted. Nothing
// about a speaker is cached in memory, so there is nothing else to clear.
func (m *Module) PurgeUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM stt_voiceprints WHERE user_id = ?`, userID)
	return err
}

// PurgeUserHousehold deletes a user's voiceprints in one household they left (D20).
func (m *Module) PurgeUserHousehold(ctx context.Context, tx *sql.Tx, userID int64, householdID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM stt_voiceprints WHERE user_id = ? AND household_id = ?`, userID, householdID)
	return err
}

// PurgeHousehold deletes a deleted household's voiceprints, inside tx (D49).
func (m *Module) PurgeHousehold(ctx context.Context, tx *sql.Tx, householdID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM stt_voiceprints WHERE household_id = ?`, householdID)
	return err
}

// purgeUserHouseholds is the HTTP account purge: PurgeUser plus the households it touched.
func (m *Module) purgeUserHouseholds(ctx context.Context, userID int64) ([]string, error) {
	households := []string{}
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT household_id FROM stt_voiceprints WHERE user_id = ? ORDER BY household_id`, userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return err
			}
			households = append(households, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return m.PurgeUser(ctx, tx, userID)
	})
	return households, err
}

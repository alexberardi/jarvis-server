package cc

import (
	"context"
	"fmt"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
)

// fakeSTT's profile store is its enrolled list ("household:user" per sample).

func (f *fakeSTT) Samples(_ context.Context, hh string, uid int64) ([]stt.Sample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []stt.Sample
	for i, e := range f.enrolled {
		if e == fmt.Sprintf("%s:%d", hh, uid) {
			out = append(out, stt.Sample{Index: i})
		}
	}
	return out, nil
}

func (f *fakeSTT) DeleteProfile(_ context.Context, hh string, uid int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []string
	for _, e := range f.enrolled {
		if e != fmt.Sprintf("%s:%d", hh, uid) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(f.enrolled) {
		return stt.ErrNoProfile
	}
	f.enrolled = kept
	return nil
}

func TestMobileVoiceProfileStatusAndDelete(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	alex, sam := bearer("tok-7"), bearer("tok-8")
	ve.auth.addUser(9, "hh-other", "member")
	outsider := bearer("tok-9")
	ve.stt.enrolled = []string{voiceHH + ":7", voiceHH + ":7", voiceHH + ":8", "hh-other:7"}

	const status = "/api/v0/mobile/voice-profile/status?household_id=" + voiceHH
	r := ve.do("GET", status, nil, alex).want(200).json()
	if r["has_profile"] != true || r["sample_count"] != 2.0 || r["recognition_enabled"] != false {
		t.Fatalf("status %v", r)
	}
	ve.stt.recognition = true
	if r = ve.do("GET", status, nil, alex).want(200).json(); r["recognition_enabled"] != true {
		t.Fatalf("status %v", r)
	}

	// Auth and validation: no token, a non-member (cross-household), a missing household.
	ve.do("GET", status, nil, nil).want(401)
	ve.do("GET", status, nil, outsider).detail(403, "User is not a member of this household")
	ve.do("DELETE", "/api/v0/mobile/voice-profile?household_id="+voiceHH, nil, outsider).want(403)
	if d := ve.do("GET", "/api/v0/mobile/voice-profile/status", nil, alex).want(400).json()["details"]; fmt.Sprint(d) != "[query -> household_id: Field required]" {
		t.Fatalf("validation %v", d)
	}

	// V6 deletes only the caller's samples in that household.
	r = ve.do("DELETE", "/api/v0/mobile/voice-profile?household_id="+voiceHH, nil, alex).want(200).json()
	if r["status"] != "deleted" || r["user_id"] != 7.0 || r["household_id"] != voiceHH {
		t.Fatalf("delete %v", r)
	}
	if fmt.Sprint(ve.stt.enrolled) != "["+voiceHH+":8 hh-other:7]" {
		t.Fatalf("left %v", ve.stt.enrolled)
	}
	if r = ve.do("GET", status, nil, alex).want(200).json(); r["has_profile"] != false || r["sample_count"] != 0.0 {
		t.Fatalf("status after delete %v", r)
	}
	if r = ve.do("GET", status, nil, sam).want(200).json(); r["sample_count"] != 1.0 {
		t.Fatalf("sam's profile %v", r)
	}
	// Nothing left to delete: 404 (D8).
	ve.do("DELETE", "/api/v0/mobile/voice-profile?household_id="+voiceHH, nil, alex).detail(404, "Voice profile not found")
}

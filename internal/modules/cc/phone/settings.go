package phone

import (
	"context"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Setting keys (settings_definitions.py:776-859). M12 drops phone_calls.attempt_cap.
// web_search.enabled and household.location are declared by the voice pipeline (5b).
const (
	SettingEnabled        = "phone_calls.enabled"
	SettingCallContext    = "phone_calls.call_context"
	SettingPlanTTL        = "phone_calls.plan_ttl_minutes"
	SettingRetentionDays  = "phone_calls.audio_retention_days"
	SettingMaxCallSeconds = "phone_calls.max_call_seconds"
	SettingCallsPerDay    = "phone_calls.calls_per_day"
	SettingMonthlyMinutes = "phone_calls.monthly_minutes_cap"
	SettingMaxConcurrent  = "phone_calls.max_concurrent_calls"

	settingWebSearch = "web_search.enabled"
	settingLocation  = "household.location"
)

// Definitions are the phone settings, appended to cc's.
func Definitions() []settings.Definition {
	return []settings.Definition{
		{Key: SettingEnabled, Category: "phone_calls", Type: settings.Bool, Default: false,
			Description: "Master toggle for AI phone calls (default OFF, fail-closed)"},
		{Key: SettingCallContext, Category: "phone_calls", Type: settings.String, Default: "", IsSecret: true,
			Description: "Per-user details the call agent may use (JSON). Scoped by " +
				"user_id, not household: insurance and callback numbers are " +
				"personal. See app/services/call_context.py for the shape, the " +
				"category (is it loaded at all) and tier (may it be volunteered) " +
				"controls. Contains PII — anything reaching a call can be spoken " +
				"aloud and lands in the transcript and recording."},
		{Key: SettingPlanTTL, Category: "phone_calls", Type: settings.Int, Default: int64(20),
			Description: "Minutes a call plan (confirm card) stays valid before expiring"},
		// D42: declared because mobile's household settings screen writes it; nothing reads it.
		{Key: SettingRetentionDays, Category: "phone_calls", Type: settings.Int, Default: int64(30),
			Description: "Days to retain call audio AND transcript before reaping"},
		{Key: SettingMaxCallSeconds, Category: "phone_calls", Type: settings.Int, Default: int64(600),
			Description: "Hard cap on a single call's duration (reaper enforces)"},
		{Key: SettingCallsPerDay, Category: "phone_calls", Type: settings.Int, Default: int64(10),
			Description: "Per-household daily call/plan cap (also caps pending plans)"},
		{Key: SettingMonthlyMinutes, Category: "phone_calls", Type: settings.Int, Default: int64(60),
			Description: "Per-household monthly call minutes cap (fail-closed when exceeded)"},
		{Key: SettingMaxConcurrent, Category: "phone_calls", Type: settings.Int, Default: int64(1),
			Description: "Max simultaneous active calls per household"},
	}
}

// Enabled is the fail-closed master gate: no household, a settings error or a non-true
// value all mean disabled.
func (s *Service) Enabled(ctx context.Context, hh string) bool {
	return s.boolSetting(ctx, SettingEnabled, hh)
}

func (s *Service) boolSetting(ctx context.Context, key, hh string) bool {
	if hh == "" || s.Settings == nil {
		return false
	}
	v, err := s.Settings.Get(ctx, key, settings.Scope{HouseholdID: hh})
	if err != nil {
		s.log().Warn("phone: gate check failed, defaulting DISABLED", "key", key, "err", err)
		return false
	}
	switch x := v.Value.(type) {
	case bool:
		return x
	case string:
		l := strings.ToLower(x)
		return l == "true" || l == "1" || l == "yes"
	}
	return false
}

// intSetting is _int_setting: the default for a missing, boolean or unreadable value.
func (s *Service) intSetting(ctx context.Context, key, hh string, def int64) int64 {
	if s.Settings == nil {
		return def
	}
	v, err := s.Settings.Get(ctx, key, settings.Scope{HouseholdID: hh})
	if err != nil || v.Value == nil {
		return def
	}
	switch x := v.Value.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return def
}

func (s *Service) location(ctx context.Context, hh string) string {
	if hh == "" || s.Settings == nil {
		return ""
	}
	v, err := s.Settings.Get(ctx, settingLocation, settings.Scope{HouseholdID: hh})
	if err != nil || v.Value == nil {
		return ""
	}
	str, _ := v.Value.(string)
	return strings.TrimSpace(str)
}

// loadCallContext reads a user's stored context; any failure degrades to none.
func (s *Service) loadCallContext(ctx context.Context, userID *int64) []ContextField {
	if userID == nil || s.Settings == nil {
		return nil
	}
	v, err := s.Settings.Get(ctx, SettingCallContext, settings.Scope{UserID: *userID})
	if err != nil {
		s.log().Warn("phone: call context lookup failed", "user", *userID, "err", err)
		return nil
	}
	return ParseCallContext(v.Value)
}

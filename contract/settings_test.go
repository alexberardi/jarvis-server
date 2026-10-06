//go:build contract

package contract

import (
	"net/http"
	"testing"
)

// The /settings router shared by every service (jarvis-settings-client's create_settings_router;
// jarvisd's internal/platform/settings mirrors it). Reads take a superuser JWT or app
// credentials, except on config-service, whose reads are superuser-only.

var settingShape = Obj{
	"key":             NonEmptyString,
	"value":           Any,
	"value_type":      OneOf("string", "int", "float", "bool", "json"),
	"category":        NonEmptyString,
	"description":     NullOr(String),
	"requires_reload": Bool,
	"is_secret":       Bool,
	"env_fallback":    NullOr(String),
	"from_db":         Bool,
	"options":         NullOr(Array),
}

var settingsList = Obj{"settings": ArrayOf(settingShape), "total": Int}

var settingNotFound = func(key string) Matcher {
	return Obj{"detail": Obj{"error": Obj{
		"type":    Eq("not_found"),
		"message": Eq("Setting not found: " + key),
		"code":    Eq("not_found"),
	}}}
}

// appSettingsListeners serve /settings with combined (superuser JWT or app) auth.
// notifications has no /settings router.
var appSettingsListeners = []string{Auth, Logs, CommandCenter, LLM, Whisper, TTS, Recipes}

func TestSettingsAppAuth(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	user := SharedUser(t)

	for _, l := range appSettingsListeners {
		t.Run(l, func(t *testing.T) {
			tg.Need(t, l)

			r := tg.Get(t, l, "/settings/", app.H()).Expect(http.StatusOK, settingsList)
			obj := r.Object()
			list := obj["settings"].([]any)
			if mustFloat(obj["total"]) != float64(len(list)) {
				r.Fatalf("total should equal len(settings)")
			}
			if len(list) == 0 {
				r.Fatalf("every service defines settings; got none")
			}
			for _, s := range list {
				m := s.(map[string]any)
				// Secrets that have a value are masked.
				if m["is_secret"] == true && m["value"] != nil && m["value"] != "" && m["value"] != "********" {
					r.Fatalf("secret %v is not masked", m["key"])
				}
			}

			// One key, read back individually.
			first := list[0].(map[string]any)
			key := first["key"].(string)
			one := tg.Get(t, l, "/settings/"+key, app.H()).Expect(http.StatusOK, settingShape).Object()
			if one["key"] != key || one["category"] != first["category"] || one["value_type"] != first["value_type"] {
				r.Fatalf("GET /settings/%s disagrees with the list entry: %v", key, one)
			}

			tg.Get(t, l, "/settings/categories", app.H()).Expect(http.StatusOK, Obj{"categories": NonEmptyArrayOf(NonEmptyString)})

			tg.Get(t, l, "/settings/contract.no.such.key", app.H()).
				ExpectStatus(http.StatusNotFound).ExpectShape(settingNotFound("contract.no.such.key"))

			tg.Get(t, l, "/settings/").
				ExpectError(http.StatusUnauthorized, "Missing authentication. Provide either Bearer token or app credentials.")
			tg.Get(t, l, "/settings/", H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong"}).
				ExpectError(http.StatusUnauthorized, "Invalid app credentials")
			tg.Get(t, l, "/settings/", user.H()).
				ExpectError(http.StatusForbidden, "Superuser access required")
		})
	}
}

// TestSettingsOptionsOnSingleRead: the list carries each definition's options; the single-key
// read drops them.
func TestSettingsOptionsOnSingleRead(t *testing.T) {
	tg := T(t)
	app := SharedApp(t)
	tg.Need(t, CommandCenter)
	list := tg.Get(t, CommandCenter, "/settings/", app.H()).ExpectStatus(http.StatusOK).Object()["settings"].([]any)
	for _, s := range list {
		m := s.(map[string]any)
		if m["options"] == nil {
			continue
		}
		// LEGACY-BUG: GET /settings/{key} never passes options to SettingResponse, so it is
		// always null even when the list shows options for the same key.
		tg.Get(t, CommandCenter, "/settings/"+m["key"].(string), app.H()).
			Expect(http.StatusOK, All(settingShape, Open{"options": Null}))
		return
	}
	t.Skip("no setting with options on this target")
}

func TestSettingsConfigService(t *testing.T) {
	tg := T(t)
	tg.Need(t, Config)
	app := SharedApp(t)
	super := SharedSuperuser(t)
	user := SharedUser(t)

	tg.Get(t, Config, "/settings/", super.H()).Expect(http.StatusOK, settingsList)
	tg.Get(t, Config, "/settings/categories", super.H()).Expect(http.StatusOK, Obj{"categories": ArrayOf(NonEmptyString)})

	// config-service reads are superuser-only: app credentials alone are not enough.
	tg.Get(t, Config, "/settings/", app.H()).ExpectError(http.StatusUnauthorized, "Missing or invalid Authorization header")
	tg.Get(t, Config, "/settings/").ExpectError(http.StatusUnauthorized, "Missing or invalid Authorization header")
	tg.Get(t, Config, "/settings/", user.H()).ExpectError(http.StatusForbidden, "Superuser access required")
	tg.Get(t, Config, "/settings/", Bearer("not-a-jwt")).ExpectError(http.StatusUnauthorized, "Invalid or expired token")
}

func TestSettingsSuperuserRead(t *testing.T) {
	tg := T(t)
	super := SharedSuperuser(t)
	for _, l := range []string{Auth, Logs, CommandCenter} {
		t.Run(l, func(t *testing.T) {
			tg.Need(t, l)
			tg.Get(t, l, "/settings/", super.H()).Expect(http.StatusOK, settingsList)
		})
	}
}

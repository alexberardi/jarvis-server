//go:build contract

package contract

import (
	"fmt"
	"net/http"
	"testing"
)

// jarvisd's admin listener (7710): the superuser gate, the bootstrap allow-list, the
// pass-through gateway and the BFF (docs/admin/00-inventory.md §3, §6, §9). Legacy jarvis-admin
// (Fastify) is replaced, not ported, so there is no Python oracle: these shapes freeze what the
// embedded SPA consumes (web/admin/src/api, src/types). The pass-through paths inherit their
// modules' contract tests; here they are checked only for the rewrite and the gate.

// adminTarget skips unless the target is jarvisd and runs the admin listener.
func adminTarget(t *testing.T) *Target {
	t.Helper()
	tg := T(t)
	if !Jarvisd() {
		t.Skip("contract: the admin BFF is jarvisd-only (no legacy equivalent); set " + EnvImpl + "=jarvisd")
	}
	tg.Need(t, Admin, Auth)
	return tg
}

// adminLogin signs in through the admin listener's bootstrap route and returns the token.
func adminLogin(t *testing.T, tg *Target, u *User) string {
	t.Helper()
	r := tg.Post(t, Admin, "/api/auth/login", map[string]string{"email": u.Email, "password": u.Password}).
		Expect(http.StatusOK, All(tokenResponse, Open{"user": Open{"id": Eq(u.ID), "is_superuser": Eq(u.Superuser)}}))
	return r.Object()["access_token"].(string)
}

var aggregatedSettings = Obj{
	"services": ArrayOf(Obj{
		"service_name": NonEmptyString,
		"display_name": NonEmptyString,
		"success":      Bool,
		"settings":     ArrayOf(settingShape),
		"error":        NullOr(String),
		"latency_ms":   NullOr(Num),
	}),
	"total_services":      Int,
	"successful_services": Int,
	"failed_services":     Int,
}

var adminDevice = Open{"backend": String, "index": Int, "id": String, "name": String, "total_mb": Int, "free_mb": Int}

var adminHardware = Open{
	"os":          NonEmptyString,
	"arch":        NonEmptyString,
	"devices":     NullOr(ArrayOf(adminDevice)),
	"sources":     NullOr(ArrayOf(String)),
	"flavour":     String,
	"detected_at": TimestampUTC,
}

// hardwareResponse is the model manager's GET /v1/hardware (SPA HardwareResponse).
var hardwareResponse = Obj{
	"hardware": adminHardware,
	"proposal": MapOf(Obj{"gpu_backend": String, "gpu_devices": String}),
	"builds":   MapOf(Obj{"build": NonEmptyString, "flavours": ArrayOf(String)}),
	"installed": NullOr(ArrayOf(Open{
		"kind": String, "build": String, "flavour": String, "path": String, "pinned": Bool,
	})),
	"engines": NullOr(ArrayOf(Open{
		"name": String, "state": String, "pid": Int, "restarts": Int, "kind": String, "flavour": String,
		"port": Int, "model": String, "labels": NullOr(ArrayOf(String)), "args": NullOr(ArrayOf(String)),
	})),
	"voice": NullOr(ArrayOf(Open{"label": NonEmptyString, "id": String, "kind": NonEmptyString, "path": String})),
}

var traceListItem = Open{
	"id":                UUID,
	"conversation_id":   String,
	"request_type":      String,
	"source":            String,
	"node_id":           NullOr(String),
	"household_id":      NullOr(String),
	"user_command":      NullOr(String),
	"assistant_message": NullOr(String),
	"status":            String,
	"total_duration_ms": Num,
	"span_count":        Int,
	"created_at":        String,
}

// setupStateAnon is GET /api/setup/state without a superuser token.
var setupStateAnon = Obj{
	"needs_superuser":      Bool,
	"setup_token_required": Bool,
	"version":              NonEmptyString,
	"superuser":            Eq(false),
	"setup_token_file":     Optional(NonEmptyString),
}

// setupStateSuper is the superuser view: the wizard's resume point and readiness summary.
var setupStateSuper = Obj{
	"needs_superuser":      Eq(false),
	"setup_token_required": Eq(false),
	"version":              NonEmptyString,
	"superuser":            Eq(true),
	"labels":               MapOf(NonEmptyString), // label -> state ("ready", "not_configured", …)
	"live_ready":           Bool,
	"models_configured":    Bool,
	"hardware": NullOr(Open{
		"hardware": adminHardware,
		"proposal": MapOf(Obj{"gpu_backend": String, "gpu_devices": String}),
		"flavours": MapOf(ArrayOf(String)),
	}),
	"hardware_url":    Eq("/api/llm/v1/hardware"),
	"prompt_provider": NullOr(Open{"value": String, "effective": String, "source": String, "options": ArrayOf(String)}),
	"doctor":          Obj{"status": OneOf("ok", "warn", "fail"), "failing": ArrayOf(String), "ran_at": TimestampUTC},
	"households":      Int,
	"nodes":           Int,
	"setup_completed": Bool,
	"setup_step":      OneOf("hardware", "models", ""),
}

func TestAdminGate(t *testing.T) {
	tg := adminTarget(t)
	user := SharedUser(t)
	userTok := adminLogin(t, tg, user)

	// Gated BFF routes, pass-through routes and an unknown /api path all share the gate, so an
	// anonymous caller can't tell them apart.
	for _, path := range []string{"/api/settings", "/api/admin/users", "/api/llm/v1/hardware", "/api/traces", "/api/contract-no-such-route"} {
		t.Run(path, func(t *testing.T) {
			tg.Get(t, Admin, path).ExpectError(http.StatusUnauthorized, "Missing or invalid Authorization header")
			tg.Get(t, Admin, path, Bearer("not-a-jwt")).ExpectError(http.StatusUnauthorized, "Invalid or expired token")
			tg.Get(t, Admin, path, Bearer(userTok)).ExpectError(http.StatusForbidden, "Superuser access required")
		})
	}

	t.Run("bootstrap_allow_list", func(t *testing.T) {
		tg.Get(t, Admin, "/api/auth/setup-status").Expect(http.StatusOK, Obj{"needs_setup": Eq(false)})
		tg.Get(t, Admin, "/api/setup/state").Expect(http.StatusOK, All(setupStateAnon, Open{"needs_superuser": Eq(false)}))
		tg.Get(t, Admin, "/api/setup/state", Bearer(userTok)).Expect(http.StatusOK, setupStateAnon)
		tg.Post(t, Admin, "/api/auth/login", map[string]string{"email": user.Email, "password": "contract-wrong-1"}).
			ExpectError(http.StatusUnauthorized, "Invalid email or password")
	})
}

func TestAdminBFF(t *testing.T) {
	tg := adminTarget(t)
	super := SharedSuperuser(t)
	super.Superuser = true
	user := SharedUser(t)
	h := Bearer(adminLogin(t, tg, super))

	t.Run("me", func(t *testing.T) {
		tg.Get(t, Admin, "/api/auth/me", h).Expect(http.StatusOK, All(userOut, Open{"id": Eq(super.ID), "is_superuser": Eq(true)}))
	})

	t.Run("settings", func(t *testing.T) {
		all := tg.Get(t, Admin, "/api/settings", h).Expect(http.StatusOK, aggregatedSettings).Object()
		names := map[string]bool{}
		for _, s := range all["services"].([]any) {
			names[s.(map[string]any)["service_name"].(string)] = true
		}
		for _, want := range []string{"admin", "auth", "cc"} {
			if !names[want] {
				t.Fatalf("/api/settings: service %q missing from %v", want, names)
			}
		}
		one := tg.Get(t, Admin, "/api/settings?service=admin", h).Expect(http.StatusOK, aggregatedSettings).Object()
		if n := fmt.Sprint(one["total_services"]); n != "1" {
			t.Fatalf("?service=admin: total_services %v, want 1", n)
		}
		tg.Get(t, Admin, "/api/settings?service=contract-nope", h).ExpectError(http.StatusNotFound, "Service 'contract-nope' not found")

		// A no-op write: the current value of admin's updates.enabled back to itself.
		var cur any
		for _, s := range one["services"].([]any)[0].(map[string]any)["settings"].([]any) {
			if s.(map[string]any)["key"] == "updates.enabled" {
				cur = s.(map[string]any)["value"]
			}
		}
		if cur == nil {
			t.Fatalf("admin settings lack updates.enabled: %v", one)
		}
		tg.Do(t, Admin, http.MethodPut, "/api/settings/admin/updates.enabled", map[string]any{"value": cur}, h).
			Expect(http.StatusOK, Obj{
				"service_name": Eq("admin"), "success": Eq(true), "key": Eq("updates.enabled"),
				"requires_reload": Bool, "message": NullOr(String), "error": Null,
			})
	})

	t.Run("admin_users", func(t *testing.T) {
		list := tg.Get(t, Admin, "/api/admin/users", h).ExpectStatus(http.StatusOK).JSON()
		if errs := NonEmptyArrayOf(superuserUserItem).Match("$", list); len(errs) > 0 {
			t.Fatalf("shape: %v", errs)
		}
		if findBy(list.([]any), "id", user.ID) == nil {
			t.Fatalf("/api/admin/users is /superuser/users: user %d missing", user.ID)
		}
	})

	t.Run("hardware", func(t *testing.T) {
		tg.Get(t, Admin, "/api/llm/v1/hardware", h).Expect(http.StatusOK, hardwareResponse)
	})

	t.Run("traces", func(t *testing.T) {
		tg.Get(t, Admin, "/api/traces?limit=5", h).
			Expect(http.StatusOK, Obj{"traces": ArrayOf(traceListItem), "total": Int})
		// cc's validation body, which the Fastify proxy passed through to the SPA.
		tg.Get(t, Admin, "/api/traces?limit=abc", h).Expect(http.StatusBadRequest, Obj{
			"error":   Eq("validation_error"),
			"message": Eq("Request validation failed. Please correct the highlighted fields."),
			"details": NonEmptyArrayOf(String),
		})
		tg.Get(t, Admin, "/api/traces/00000000-0000-0000-0000-000000000000", h).ExpectError(http.StatusNotFound, "Trace not found")
	})

	t.Run("setup_state", func(t *testing.T) {
		st := tg.Get(t, Admin, "/api/setup/state", h).Expect(http.StatusOK, setupStateSuper).Object()
		if _, ok := st["labels"].(map[string]any)["live"]; !ok {
			t.Fatalf("setup state labels lack live: %v", st["labels"])
		}
	})

	t.Run("unknown_api_path", func(t *testing.T) {
		// /api never falls through to the SPA: a superuser gets a JSON 404.
		tg.Get(t, Admin, "/api/contract-no-such-route", h).ExpectJSONContentType().ExpectError(http.StatusNotFound, "Not Found")
	})
}

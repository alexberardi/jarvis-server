//go:build contract

package contract

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Command-center node routes (docs/cc/05 §2.1, §3.1, §3.11; doc 00 §3.1 and §3.5). Every
// route here takes node auth, X-API-Key: node_id:node_key, validated against jarvis-auth's
// /internal/validate-node with service_id = CC's app id, plus a CC nodes row.

// ccServiceID is command-center's app id in jarvis-auth, the service a node needs a grant for.
const ccServiceID = "jarvis-command-center"

// ccValidation matches CC's custom validation body: 400 (not 422) with flattened details,
// docs/cc/00 §3.5. want is one of the "loc -> loc: msg" lines, matched by prefix.
func ccValidation(wantPrefix string) Matcher {
	return MatchFunc(func(path string, v any) []string {
		errs := Obj{
			"error":   Eq("validation_error"),
			"message": Eq("Request validation failed. Please correct the highlighted fields."),
			"details": NonEmptyArrayOf(String),
		}.Match(path, v)
		if len(errs) > 0 {
			return errs
		}
		for _, d := range v.(map[string]any)["details"].([]any) {
			if strings.HasPrefix(d.(string), wantPrefix) {
				return nil
			}
		}
		return mismatch(path+".details[*]", "an entry starting "+show(wantPrefix), v)
	})
}

func TestCCNodeAuth(t *testing.T) {
	tg := T(t)
	n := SharedCCNode(t)
	u := SharedUser(t)
	const path = "/api/v0/node/mqtt-credentials"

	t.Run("missing_header", func(t *testing.T) {
		// FastAPI Header(...) is required; CC's handler turns the 422 into a 400.
		tg.Get(t, CommandCenter, path).
			Expect(http.StatusBadRequest, ccValidation("header -> x-api-key: "))
		// A user JWT is not node auth.
		tg.Get(t, CommandCenter, path, u.H()).
			Expect(http.StatusBadRequest, ccValidation("header -> x-api-key: "))
	})
	t.Run("bare_key_not_found", func(t *testing.T) {
		// No colon: the legacy bare-node_key lookup (D40 Q10 drops it in Go).
		tg.Get(t, CommandCenter, path, H{"X-API-Key": "contract-garbage-" + randHex(4)}).
			ExpectError(http.StatusUnauthorized, "Invalid API Key")
	})
	t.Run("wrong_key", func(t *testing.T) {
		tg.Get(t, CommandCenter, path, H{"X-API-Key": n.ID + ":wrong-" + randHex(4)}).
			ExpectError(http.StatusUnauthorized, "Invalid node credentials")
	})
	t.Run("unknown_node", func(t *testing.T) {
		tg.Get(t, CommandCenter, path, H{"X-API-Key": "contract-nosuch-" + randHex(4) + ":k"}).
			ExpectError(http.StatusUnauthorized, "Node not found")
	})
	t.Run("not_configured_locally", func(t *testing.T) {
		// Valid in auth (with the CC grant) but no CC row.
		an := NewNode(t, u.HouseholdID, ccServiceID)
		tg.Get(t, CommandCenter, path, an.APIKeyH()).
			ExpectError(http.StatusUnauthorized, "Node not configured locally")
	})
	t.Run("no_service_grant", func(t *testing.T) {
		// validate-node's reason passes through verbatim as the 401 detail.
		an := NewNode(t, u.HouseholdID, "jarvis-logs")
		tg.Get(t, CommandCenter, path, an.APIKeyH()).
			ExpectError(http.StatusUnauthorized, "Node is not authorized to access service '"+ccServiceID+"'")
	})
	t.Run("user_jwt_routes", func(t *testing.T) {
		// verify_user_jwt's details, on a mobile route that takes a JWT.
		r := tg.Post(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/settings/requests", nil)
		r.ExpectError(http.StatusUnauthorized, "Missing or invalid Authorization header")
		if got := r.Header["Www-Authenticate"]; len(got) == 0 || got[0] != "Bearer" {
			r.Fatalf("want WWW-Authenticate: Bearer, got %v", got)
		}
		tg.Post(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/settings/requests", nil, Bearer("not.a.jwt")).
			ExpectError(http.StatusUnauthorized, "Invalid token")
	})
	t.Run("admin_key", func(t *testing.T) {
		tg.Post(t, CommandCenter, "/api/v0/admin/nodes", map[string]any{
			"node_id": "contract-never", "household_id": u.HouseholdID, "room": "x",
		}, H{"X-API-Key": "wrong-admin-key"}).
			ExpectError(http.StatusUnauthorized, "Invalid Admin API Key")
	})
}

// nodeResponseShape is admin.py NodeResponse (mobile getNode / listNodes). jarvisd drops the
// LoRA-only adapter_hash (D9; mobile's node type never reads it).
func nodeResponseShape() Obj {
	shape := legacyNodeResponseShape()
	if Jarvisd() {
		delete(shape, "adapter_hash")
	}
	return shape
}

func legacyNodeResponseShape() Obj { return Obj{
	"node_id":    NonEmptyString,
	"room":       String,
	"user":       String,
	"voice_mode": NullOr(String),
	// LoRA leftover; D9 drops it once mobile is confirmed to ignore it.
	"adapter_hash": NullOr(String),
	"household_id": NullOr(String),
	"online":       Bool,
	// LEGACY-BUG: naive timestamp (datetime.utcnow()), no zone.
	"last_seen":         NullOr(TimestampNaive),
	"last_seen_version": NullOr(String),
	"install_mode":      NullOr(String),
	"git_sha":           NullOr(String),
	"is_busy":           Bool,
	"needs_k2":          Bool,
}}

func TestCCNodeCreateAndHeartbeat(t *testing.T) {
	tg := T(t)
	u := SharedUser(t)
	n := NewCCNode(t, u)

	// Admin create (in the fixture) answers 200, not 201 (doc 05 §7.14), and a second create
	// of the same id is refused before auth is called.
	tg.Post(t, CommandCenter, "/api/v0/admin/nodes", map[string]any{
		"node_id": n.ID, "household_id": u.HouseholdID, "room": "contract",
	}, CCAdminH()).ExpectError(http.StatusBadRequest, "Node already exists locally")

	got := tg.Get(t, CommandCenter, "/api/v0/admin/nodes/"+n.ID, u.H()).
		Expect(http.StatusOK, nodeResponseShape()).Object()
	if got["node_id"] != n.ID || got["room"] != "contract" || got["user"] != "default" ||
		got["voice_mode"] != "brief" || got["household_id"] != u.HouseholdID || got["needs_k2"] != true {
		t.Fatalf("fresh node fields: %v", got)
	}

	// Heartbeat: every field optional, body optional, response {"status":"ok"} (plus
	// pending_update only when an update task is queued).
	tg.Post(t, CommandCenter, "/api/v0/admin/nodes/heartbeat", nil, n.APIKeyH()).
		Expect(http.StatusOK, Obj{"status": Eq("ok")})
	tg.Post(t, CommandCenter, "/api/v0/admin/nodes/heartbeat", map[string]any{
		"version_info":  map[string]any{"version": "0.0.1-contract", "install_mode": "dev", "git_sha": "c0ffee"},
		"is_busy":       true,
		"protocols":     []string{"contract"},
		"needs_k2":      false,
		"thread_status": map[string]any{"x": 1},
		"unknown_field": "ignored",
	}, n.APIKeyH()).Expect(http.StatusOK, Obj{"status": Eq("ok")})

	got = tg.Get(t, CommandCenter, "/api/v0/admin/nodes/"+n.ID, u.H()).
		Expect(http.StatusOK, nodeResponseShape()).Object()
	if got["last_seen_version"] != "0.0.1-contract" || got["install_mode"] != "dev" || got["git_sha"] != "c0ffee" ||
		got["is_busy"] != true || got["needs_k2"] != false || got["online"] != true {
		t.Fatalf("heartbeat not reflected: %v", got)
	}

	tg.Get(t, CommandCenter, "/api/v0/admin/nodes/contract-nosuch-"+randHex(3), u.H()).
		ExpectError(http.StatusNotFound, "Node not found")
}

func TestCCNodeMQTTCredentials(t *testing.T) {
	tg := T(t)
	n := SharedCCNode(t)
	// Nulls mean "connect anonymously". The shape is unchanged by D4's per-node credentials.
	tg.Get(t, CommandCenter, "/api/v0/node/mqtt-credentials", n.APIKeyH()).
		Expect(http.StatusOK, Obj{"username": NullOr(NonEmptyString), "password": NullOr(NonEmptyString)})
}

var settingsRequestShape = Obj{
	"request_id": UUID,
	"node_id":    NonEmptyString,
	"status":     OneOf("pending", "fulfilled"),
	// LEGACY-BUG: naive timestamps.
	"created_at": TimestampNaive,
	"expires_at": TimestampNaive,
}

// settingsRequestListShape is the node's pending list. jarvisd adds include_values and
// user_id, which used to ride only in the MQTT payload, so the node's reconnect backstop can
// honour secret sync (D40 05.Q8).
func settingsRequestListShape() Obj {
	if !Jarvisd() {
		return settingsRequestShape
	}
	shape := Obj{"include_values": Bool, "user_id": NullOr(Int)}
	for k, v := range settingsRequestShape {
		shape[k] = v
	}
	return shape
}

func TestCCNodeSettingsRequestsList(t *testing.T) {
	tg := T(t)
	n := SharedCCNode(t)
	tg.Get(t, CommandCenter, "/api/v0/nodes/"+n.ID+"/settings/requests", n.APIKeyH()).
		Expect(http.StatusOK, ArrayOf(settingsRequestListShape()))
	tg.Get(t, CommandCenter, "/api/v0/nodes/contract-other/settings/requests", n.APIKeyH()).
		ExpectError(http.StatusForbidden, "Cannot access other node's requests")
}

// The node's strict pydantic DateContext (jarvis-node-setup
// clients/responses/jarvis_command_center/date_context_response.py) plus the extra keys CC
// sends that the node ignores (relative_dates.last_night, time_expressions).
func ccDateContextShape(tz Matcher) Matcher {
	day := Obj{"date": Regexp(`^\d{4}-\d{2}-\d{2}$`), "utc_start_of_day": Regexp(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)}
	wday := Obj{"day": String, "date": Regexp(`^\d{4}-\d{2}-\d{2}$`), "utc_start_of_day": Regexp(`Z$`)}
	pair := All(ArrayOf(day), ccArrayLen(2))
	weekdays := Obj{}
	for _, d := range []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"} {
		weekdays["next_"+d] = day
		weekdays["last_"+d] = day
	}
	return Obj{
		"current": Obj{
			"date":             String,
			"date_iso":         Regexp(`^\d{4}-\d{2}-\d{2}$`),
			"time":             String,
			"datetime":         Regexp(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`),
			"weekday":          OneOf("monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"),
			"weekday_number":   Int,
			"utc_start_of_day": Regexp(`Z$`),
		},
		"relative_dates": Obj{
			"tomorrow": day, "yesterday": day, "day_after_tomorrow": day, "day_before_yesterday": day,
			"last_night": Obj{"date": String, "time": Eq("19:00:00"), "datetime": Regexp(`Z$`)},
		},
		"weekend":          Obj{"this_weekend": All(ArrayOf(wday), ccArrayLen(2)), "next_weekend": All(ArrayOf(wday), ccArrayLen(2)), "last_weekend": All(ArrayOf(wday), ccArrayLen(2))},
		"weeks":            Obj{"this_week": All(ArrayOf(wday), ccArrayLen(7)), "next_week": All(ArrayOf(wday), ccArrayLen(7)), "last_week": All(ArrayOf(wday), ccArrayLen(7))},
		"months":           Obj{"this_month": pair, "next_month": pair, "last_month": pair},
		"years":            Obj{"this_year": pair, "next_year": pair, "last_year": pair},
		"weekdays":         weekdays,
		"timezone":         tz,
		"time_expressions": MapOf(Regexp(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)),
	}
}

func ccArrayLen(n int) Matcher {
	return MatchFunc(func(path string, v any) []string {
		if a, ok := v.([]any); !ok || len(a) != n {
			return mismatch(path, "array of length "+show(n), v)
		}
		return nil
	})
}

func TestCCDateContext(t *testing.T) {
	tg := T(t)
	n := SharedCCNode(t)

	t.Run("with_timezone", func(t *testing.T) {
		// What the node sends on every conversation start.
		tg.Get(t, CommandCenter, "/api/v0/generate/date-context?timezone=America/New_York", n.APIKeyH()).
			Expect(http.StatusOK, ccDateContextShape(Obj{
				"user_timezone":    Eq("America/New_York"),
				"current_timezone": Eq("America/New_York"),
				"is_dst":           Bool,
			}))
	})
	t.Run("without_timezone", func(t *testing.T) {
		// LEGACY-BUG: user_timezone and is_dst are null, which the node's strict model rejects
		// (it then treats the context as None). D40 Q11: Go always fills both.
		tg.Get(t, CommandCenter, "/api/v0/generate/date-context", n.APIKeyH()).
			Expect(http.StatusOK, ccDateContextShape(Obj{
				"user_timezone":    Null,
				"current_timezone": Eq("local"),
				"is_dst":           Null,
			}))
	})
	t.Run("unknown_timezone", func(t *testing.T) {
		// LEGACY-BUG: an unknown zone is a 500 with a non-FastAPI {"error"} body.
		tg.Get(t, CommandCenter, "/api/v0/generate/date-context?timezone=Contract/Nowhere", n.APIKeyH()).
			Expect(http.StatusInternalServerError, Obj{"error": Eq("Failed to generate date context: 'Contract/Nowhere'")})
	})
	t.Run("auth", func(t *testing.T) {
		tg.Get(t, CommandCenter, "/api/v0/generate/date-context?timezone=UTC", H{"X-API-Key": n.ID + ":bad"}).
			ExpectError(http.StatusUnauthorized, "Invalid node credentials")
	})
}

// --- public plugin endpoints (doc 05 §3.11, doc 13) ---

// ccInboxItemShape is jarvis-notifications' InboxItemResponse, read back to check what
// CC actually delivered.
var ccInboxItemShape = Obj{
	"id": UUID, "user_id": NullOr(Int), "household_id": String, "title": String, "summary": String,
	"body": String, "category": String, "source_service": String, "metadata": NullOr(Object),
	"is_read": Bool, "created_at": TimestampNaive,
}

// ccReadAndDeleteInbox reads an inbox item as u (asserting the shape) and deletes it.
func ccReadAndDeleteInbox(t *testing.T, u *User, id string) map[string]any {
	t.Helper()
	tg := T(t)
	item := tg.Get(t, Notifications, "/api/v0/inbox/"+id, u.H()).Expect(http.StatusOK, ccInboxItemShape).Object()
	tg.Do(t, Notifications, http.MethodDelete, "/api/v0/inbox/"+id, nil, u.H()).ExpectStatus(http.StatusNoContent)
	return item
}

func TestCCNodePlugin(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)
	n := SharedCCNode(t)
	u := SharedUser(t)

	inboxResp := Obj{"id": NullOr(UUID), "sent": Bool, "withheld_by": NullOr(String)}

	t.Run("inbox_item", func(t *testing.T) {
		r := tg.Post(t, CommandCenter, "/api/v0/node/inbox-item", map[string]any{
			"title": "contract inbox " + tg.RunID, "summary": "s", "body": "b", "category": "contract",
			"metadata": map[string]any{"k": "v"},
		}, n.APIKeyH()).Expect(http.StatusOK, inboxResp).Object()
		if r["sent"] != true || r["id"] == nil || r["withheld_by"] != nil {
			t.Fatalf("want sent with an id: %v", r)
		}
		item := ccReadAndDeleteInbox(t, u, r["id"].(string))
		md := item["metadata"].(map[string]any)
		// household from the node identity; metadata.node_id injected server-side.
		if item["household_id"] != u.HouseholdID || md["node_id"] != n.ID || md["k"] != "v" ||
			item["category"] != "contract" || item["source_service"] != "jarvis-command-center" || item["user_id"] != nil {
			t.Fatalf("delivered item: %v", item)
		}

		// metadata.node_id uses setdefault: a caller-supplied value wins (doc 05 §7.12).
		r = tg.Post(t, CommandCenter, "/api/v0/node/inbox-item", map[string]any{
			"title": "contract inbox override", "metadata": map[string]any{"node_id": "contract-spoof"},
		}, n.APIKeyH()).Expect(http.StatusOK, inboxResp).Object()
		item = ccReadAndDeleteInbox(t, u, r["id"].(string))
		if item["metadata"].(map[string]any)["node_id"] != "contract-spoof" || item["category"] != "general" {
			t.Fatalf("override item: %v", item)
		}
	})
	t.Run("inbox_item_blank_title", func(t *testing.T) {
		// A whitespace title is a 200 with sent:false, not a 4xx.
		tg.Post(t, CommandCenter, "/api/v0/node/inbox-item", map[string]any{"title": "   "}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"id": Null, "sent": Eq(false), "withheld_by": Null})
		tg.Post(t, CommandCenter, "/api/v0/node/inbox-item", map[string]any{"summary": "no title"}, n.APIKeyH()).
			Expect(http.StatusBadRequest, ccValidation("body -> title: "))
		tg.Post(t, CommandCenter, "/api/v0/node/inbox-item", map[string]any{"title": "x", "target_type": "room"}, n.APIKeyH()).
			Expect(http.StatusBadRequest, ccValidation("body -> target_type: "))
	})
	t.Run("push_notification", func(t *testing.T) {
		r := tg.Post(t, CommandCenter, "/api/v0/node/push-notification", map[string]any{
			"title": "contract push " + tg.RunID, "body": "b",
		}, n.APIKeyH()).Expect(http.StatusOK, Obj{
			"sent": Bool, "inbox_item_id": NullOr(UUID), "withheld_by": NullOr(String),
		}).Object()
		if r["sent"] != true || r["inbox_item_id"] == nil {
			t.Fatalf("want sent: %v", r)
		}
		// Legacy delivery is push_confirmation_to_inbox: a "confirmation" card with the
		// reminder command name, no actions, and the body as summary.
		item := ccReadAndDeleteInbox(t, u, r["inbox_item_id"].(string))
		if item["category"] != "confirmation" || item["summary"] != "b" || item["body"] != "b" {
			t.Fatalf("push item: %v", item)
		}
		ccExpectShapeOf(t, "push metadata", item["metadata"], Obj{
			"command_name": Eq("reminder"), "node_id": Eq(n.ID), "actions": ccArrayLen(0), "draft": Null,
		})
	})
	t.Run("send_link", func(t *testing.T) {
		// Only http(s) URLs; anything else is a 200 sent:false.
		tg.Post(t, CommandCenter, "/api/v0/node/send-link", map[string]any{"user_id": u.ID, "url": "ftp://example.com/x"}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"sent": Eq(false), "withheld_by": Null})
		tg.Post(t, CommandCenter, "/api/v0/node/send-link", map[string]any{"url": "https://example.com"}, n.APIKeyH()).
			Expect(http.StatusBadRequest, ccValidation("body -> user_id: "))

		url := "https://example.com/contract-" + tg.RunID
		tg.Post(t, CommandCenter, "/api/v0/node/send-link", map[string]any{"user_id": u.ID, "url": url}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"sent": Eq(true), "withheld_by": Null})
		// The link lands as a user-scoped "link" card; the response carries no id, so find it.
		list := tg.Get(t, Notifications, "/api/v0/inbox?category=link", u.H()).Expect(http.StatusOK, ArrayOf(ccInboxItemShape)).JSON().([]any)
		var id string
		for _, e := range list {
			m := e.(map[string]any)
			if m["body"] == url {
				id = m["id"].(string)
			}
		}
		if id == "" {
			t.Fatalf("send-link inbox card for %s not found in %v", url, list)
		}
		item := ccReadAndDeleteInbox(t, u, id)
		if item["title"] != "Link from Jarvis" || item["summary"] != "Tap to open" {
			t.Fatalf("link item defaults: %v", item)
		}
		ccExpectShapeOf(t, "link metadata", item["metadata"], Obj{"url": Eq(url), "type": Eq("open_url")})
		if item["user_id"] == nil {
			t.Fatalf("link item should be user-scoped: %v", item)
		}
	})
	t.Run("auth", func(t *testing.T) {
		for _, p := range []string{"/api/v0/node/inbox-item", "/api/v0/node/push-notification", "/api/v0/node/send-link", "/api/v0/node/llm/chat"} {
			tg.Post(t, CommandCenter, p, map[string]any{}, H{"X-API-Key": n.ID + ":bad"}).
				ExpectError(http.StatusUnauthorized, "Invalid node credentials")
			tg.Post(t, CommandCenter, p, map[string]any{}).
				Expect(http.StatusBadRequest, ccValidation("header -> x-api-key: "))
		}
	})
}

// ExpectShapeOf matches an already-decoded value.
func ccExpectShapeOf(t testing.TB, what string, v any, m Matcher) {
	t.Helper()
	if errs := m.Match("$", v); len(errs) > 0 {
		t.Fatalf("%s: shape mismatch:\n  %s\nvalue: %s", what, strings.Join(errs, "\n  "), show(v))
	}
}

func TestCCNodeLLMChat(t *testing.T) {
	tg := T(t)
	tg.Need(t, LLM)
	n := SharedCCNode(t)
	h := tg.Get(t, LLM, "/health")
	if h.Status != http.StatusOK || h.Object()["status"] != "healthy" {
		t.Skipf("llm-proxy is not healthy; /node/llm/chat needs a live model")
	}
	tg.Post(t, CommandCenter, "/api/v0/node/llm/chat", map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "Reply with the single word: ok"}},
	}, n.APIKeyH()).Expect(http.StatusOK, Obj{"content": String})

	tg.Post(t, CommandCenter, "/api/v0/node/llm/chat", map[string]any{
		"messages": []map[string]string{{"role": "tool", "content": "x"}},
	}, n.APIKeyH()).Expect(http.StatusBadRequest, ccValidation("body -> messages -> 0 -> role: "))
}

func TestCCSignals(t *testing.T) {
	tg := T(t)
	n := SharedCCNode(t)
	// ttl_seconds keeps the row short-lived: CC's signal sweeper removes it (every 30 min),
	// and there is no delete route.
	body := func(extra map[string]any) map[string]any {
		b := map[string]any{"signal": map[string]any{
			"kind": "contract.probe", "source_key": "contract:" + tg.RunID, "ttl_seconds": 60, "source_agent": "contract-" + tg.RunID,
		}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	resp := Obj{"signal_id": Int, "mode": OneOf("open", "directed"), "proposed": Bool}

	t.Run("node_open", func(t *testing.T) {
		r := tg.Post(t, CommandCenter, "/api/v0/signals", body(nil), n.APIKeyH()).Expect(http.StatusOK, resp).Object()
		if r["mode"] != "open" || r["proposed"] != false {
			t.Fatalf("open signal: %v", r)
		}
		// An explicit household_id equal to the node's is fine.
		tg.Post(t, CommandCenter, "/api/v0/signals", body(map[string]any{"household_id": n.HouseholdID}), n.APIKeyH()).
			Expect(http.StatusOK, resp)
	})
	t.Run("household_mismatch", func(t *testing.T) {
		tg.Post(t, CommandCenter, "/api/v0/signals", body(map[string]any{"household_id": "00000000-0000-0000-0000-000000000000"}), n.APIKeyH()).
			ExpectError(http.StatusForbidden, "household_id does not match node's household")
	})
	t.Run("validation", func(t *testing.T) {
		tg.Post(t, CommandCenter, "/api/v0/signals", map[string]any{"signal": map[string]any{"kind": "x"}}, n.APIKeyH()).
			Expect(http.StatusBadRequest, ccValidation("body -> signal -> source_key: "))
	})
	t.Run("auth", func(t *testing.T) {
		const none = "Authentication required (X-Api-Key or X-Jarvis-App-Id/Key)"
		tg.Post(t, CommandCenter, "/api/v0/signals", body(nil)).ExpectError(http.StatusUnauthorized, none)
		// A failing node key is swallowed and falls through to app auth, so the detail is the
		// generic one, not validate-node's reason.
		tg.Post(t, CommandCenter, "/api/v0/signals", body(nil), H{"X-API-Key": n.ID + ":bad"}).
			ExpectError(http.StatusUnauthorized, none)
	})
	t.Run("app_auth", func(t *testing.T) {
		app := SharedApp(t)
		// LEGACY-BUG: /signals resolves jarvis-auth from env JARVIS_AUTH_URL only (never
		// discovery or JARVIS_AUTH_BASE_URL, docs/cc/00 §3.1). It is unset on the target, so
		// app-to-app auth dials localhost:7701 inside the CC container and every app call,
		// valid or not, is a 502. Go: valid app creds → 200 (no household → 400 below),
		// invalid → 401 "Invalid app credentials".
		tg.Post(t, CommandCenter, "/api/v0/signals", body(nil), app.H()).
			ExpectError(http.StatusBadGateway, "Auth service unavailable")
		tg.Post(t, CommandCenter, "/api/v0/signals", body(nil), H{"X-Jarvis-App-Id": "contract-nosuch", "X-Jarvis-App-Key": "x"}).
			ExpectError(http.StatusBadGateway, "Auth service unavailable")
	})
}

// waitFor polls f until it returns true or the deadline passes.
func ccWaitFor(d time.Duration, f func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return f()
}

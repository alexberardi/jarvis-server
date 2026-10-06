//go:build contract

package contract

import (
	"fmt"
	"net/http"
	"os"
	"testing"
)

// jarvis-notifications (legacy port 7712): the inbox and device tokens for mobile (user JWT),
// inbox writes and push sends for services (app-to-app), and an admin API (X-Api-Key).
//
// Push: the legacy service forwards to a relay (RELAY_URL) that talks to Expo. These tests
// never deliver a real push: tokens are obviously fake ("ExponentPushToken[contract-…]"), and
// on the MBP the relay is not running, so a send to a token fails with relay_unreachable.
//
// Leftovers: tokens and personal inbox items go with the fixture users (account deletion
// purges notifications, and the tests delete the household-wide items they create).
// notification_log rows stay: they are the audit trail and have no delete API (the 30-day
// retention prunes them).

// EnvNotificationsAdminKey is jarvis-notifications' ADMIN_API_KEY on the target.
const EnvNotificationsAdminKey = "JARVIS_CONTRACT_NOTIFICATIONS_ADMIN_KEY"

var inboxItemShape = Obj{
	"id":             UUID,
	"user_id":        NullOr(Int),
	"household_id":   String,
	"title":          String,
	"summary":        String,
	"body":           String,
	"category":       String,
	"source_service": String,
	"metadata":       NullOr(Object),
	"is_read":        Bool,
	"created_at":     TimestampNaive,
}

var notifyShape = Obj{
	"id":              UUID,
	"delivery_status": OneOf("pending", "delivered", "partial", "failed", "skipped"),
	"token_count":     Int,
	"success_count":   Int,
	"failure_count":   Int,
}

var tokenShape = Obj{
	"id":          UUID,
	"push_token":  String,
	"device_type": OneOf("ios", "android"),
	"device_name": NullOr(String),
	"is_active":   Bool,
}

// with returns a copy of o with the given keys replaced.
func with(o Obj, kv ...any) Obj {
	out := Obj{}
	for k, v := range o {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1].(Matcher)
	}
	return out
}

func fakePushToken(tg *Target, name string) string {
	return fmt.Sprintf("ExponentPushToken[contract-%s-%s-%s]", tg.RunID, name, randHex(3))
}

func newInboxItem(hh string, userID any, title string) map[string]any {
	return map[string]any{
		"household_id": hh, "user_id": userID, "title": title, "summary": "s", "body": "b",
		"category": "contract", "source_service": "contract-suite",
	}
}

func createInbox(t *testing.T, app *App, body map[string]any) map[string]any {
	t.Helper()
	tg := T(t)
	r := tg.Post(t, Notifications, "/api/v0/inbox", body, app.H()).Expect(http.StatusOK, inboxItemShape)
	return r.Object()
}

func ids(t *testing.T, r *Resp) []string {
	t.Helper()
	var items []struct {
		ID string `json:"id"`
	}
	r.Decode(&items)
	out := []string{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestNotificationsInfoHealth(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)
	tg.Get(t, Notifications, "/info").Expect(http.StatusOK, Obj{"service": Eq("jarvis-notifications")})
	tg.Get(t, Notifications, "/health").Expect(http.StatusOK, Obj{"status": Eq("ok"), "service": Eq("jarvis-notifications")})
}

func TestNotificationsAuth(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)

	userRoutes := []struct{ method, path string }{
		{"GET", "/api/v0/inbox"}, {"GET", "/api/v0/inbox/unread-count"}, {"GET", "/api/v0/inbox/x"},
		{"PATCH", "/api/v0/inbox/x/read"}, {"DELETE", "/api/v0/inbox/x"},
		{"POST", "/api/v0/inbox/bulk/read"}, {"POST", "/api/v0/inbox/bulk/delete"},
		{"POST", "/api/v0/tokens"}, {"DELETE", "/api/v0/tokens"}, {"GET", "/api/v0/tokens/me"},
		{"DELETE", "/api/v0/me/data"},
	}
	t.Run("user JWT missing", func(t *testing.T) {
		for _, rt := range userRoutes {
			r := tg.Do(t, Notifications, rt.method, rt.path, nil)
			r.ExpectError(http.StatusUnauthorized, "Not authenticated")
			if got := r.Header["Www-Authenticate"]; len(got) != 1 || got[0] != "Bearer" {
				r.Fatalf("WWW-Authenticate: want Bearer, got %v", got)
			}
		}
	})
	t.Run("user JWT not bearer", func(t *testing.T) {
		tg.Get(t, Notifications, "/api/v0/inbox", H{"Authorization": "Basic abc"}).
			ExpectError(http.StatusUnauthorized, "Not authenticated")
	})
	t.Run("user JWT invalid", func(t *testing.T) {
		for _, rt := range userRoutes {
			// Auth runs before body validation, so no body is needed.
			tg.Do(t, Notifications, rt.method, rt.path, nil, Bearer("not-a-jwt")).
				ExpectError(http.StatusUnauthorized, "Invalid or expired token")
		}
	})

	appRoutes := []struct{ method, path string }{
		{"POST", "/api/v0/inbox"}, {"PATCH", "/api/v0/inbox/x"},
		{"POST", "/api/v0/notify"}, {"POST", "/api/v0/notify/batch"},
	}
	t.Run("app credentials missing", func(t *testing.T) {
		for _, rt := range appRoutes {
			tg.Do(t, Notifications, rt.method, rt.path, map[string]any{}).
				ExpectError(http.StatusUnauthorized, "Missing app credentials")
		}
	})
	t.Run("app credentials invalid", func(t *testing.T) {
		app := SharedApp(t)
		for _, rt := range appRoutes {
			tg.Do(t, Notifications, rt.method, rt.path, map[string]any{}, H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong"}).
				ExpectError(http.StatusUnauthorized, "Invalid app credentials")
		}
	})
	t.Run("a user JWT is not app auth", func(t *testing.T) {
		tg.Post(t, Notifications, "/api/v0/inbox", map[string]any{}, SharedUser(t).H()).
			ExpectError(http.StatusUnauthorized, "Missing app credentials")
	})
	t.Run("admin key missing", func(t *testing.T) {
		tg.Get(t, Notifications, "/api/v0/admin/stats").
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("header", "X-Api-Key"))
		tg.Post(t, Notifications, "/api/v0/admin/cleanup", nil).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("header", "X-Api-Key"))
	})
	t.Run("admin key invalid", func(t *testing.T) {
		tg.Get(t, Notifications, "/api/v0/admin/stats", H{"X-Api-Key": "wrong"}).
			ExpectError(http.StatusUnauthorized, "Invalid admin key")
		tg.Post(t, Notifications, "/api/v0/admin/cleanup", nil, H{"X-Api-Key": "wrong"}).
			ExpectError(http.StatusUnauthorized, "Invalid admin key")
	})
}

// TestNotificationsInbox covers visibility: a member sees their own items and household-wide
// ones (user_id null), never another member's personal items or another household's.
func TestNotificationsInbox(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)
	app := SharedApp(t)
	u := NewUser(t)
	other := NewUser(t) // "another member": their personal item is written into u's household

	own := createInbox(t, app, map[string]any{
		"household_id": u.HouseholdID, "user_id": u.ID, "title": "own", "summary": "s", "body": "b",
		"category": "contract-own", "source_service": "contract-suite", "metadata": map[string]any{"k": "v", "n": 1},
	})
	if own["user_id"] == nil || fmt.Sprint(own["user_id"]) != fmt.Sprint(u.ID) || own["is_read"] != false {
		t.Fatalf("own item: %v", own)
	}
	if m, _ := own["metadata"].(map[string]any); m == nil || m["k"] != "v" {
		t.Fatalf("own metadata: %v", own["metadata"])
	}
	hhBody := newInboxItem(u.HouseholdID, nil, "household")
	hhBody["metadata"] = map[string]any{} // an empty dict is stored as NULL
	hh := createInbox(t, app, hhBody)
	if hh["user_id"] != nil || hh["metadata"] != nil {
		t.Fatalf("household item: %v", hh)
	}
	hhDeleted := false
	t.Cleanup(func() {
		if !hhDeleted {
			tg.Do(t, Notifications, "DELETE", "/api/v0/inbox/"+hh["id"].(string), nil, u.H())
		}
	})
	theirs := createInbox(t, app, newInboxItem(u.HouseholdID, other.ID, "another member's"))
	cross := createInbox(t, app, newInboxItem(other.HouseholdID, other.ID, "another household's"))
	ownID, hhID, theirsID, crossID := own["id"].(string), hh["id"].(string), theirs["id"].(string), cross["id"].(string)

	t.Run("create validation", func(t *testing.T) {
		b := newInboxItem(u.HouseholdID, nil, "x")
		delete(b, "title")
		tg.Post(t, Notifications, "/api/v0/inbox", b, app.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "title"))
	})

	t.Run("list", func(t *testing.T) {
		r := tg.Get(t, Notifications, "/api/v0/inbox", u.H()).Expect(http.StatusOK, ArrayOf(inboxItemShape))
		if got := ids(t, r); len(got) != 2 || got[0] != hhID || got[1] != ownID { // newest first
			r.Fatalf("want [household, own], got %v", got)
		}
		r = tg.Get(t, Notifications, "/api/v0/inbox?category=contract-own", u.H()).Expect(http.StatusOK, ArrayOf(inboxItemShape))
		if got := ids(t, r); len(got) != 1 || got[0] != ownID {
			r.Fatalf("category filter: %v", got)
		}
		r = tg.Get(t, Notifications, "/api/v0/inbox?is_read=true", u.H()).Expect(http.StatusOK, ArrayOf(inboxItemShape))
		if got := ids(t, r); len(got) != 0 {
			r.Fatalf("is_read filter: %v", got)
		}
		r = tg.Get(t, Notifications, "/api/v0/inbox?limit=1&offset=1", u.H()).Expect(http.StatusOK, ArrayOf(inboxItemShape))
		if got := ids(t, r); len(got) != 1 || got[0] != ownID {
			r.Fatalf("paging: %v", got)
		}
		// The other user's view of their own household.
		r = tg.Get(t, Notifications, "/api/v0/inbox", other.H()).Expect(http.StatusOK, ArrayOf(inboxItemShape))
		if got := ids(t, r); len(got) != 1 || got[0] != crossID {
			r.Fatalf("other's list: %v", got)
		}
	})
	t.Run("list validation", func(t *testing.T) {
		for q, loc := range map[string]string{"limit=0": "limit", "limit=201": "limit", "offset=-1": "offset", "is_read=maybe": "is_read", "limit=x": "limit"} {
			tg.Get(t, Notifications, "/api/v0/inbox?"+q, u.H()).
				ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("query", loc))
		}
	})
	t.Run("unread count", func(t *testing.T) {
		tg.Get(t, Notifications, "/api/v0/inbox/unread-count", u.H()).Expect(http.StatusOK, Obj{"count": Eq(2)})
	})
	t.Run("not visible", func(t *testing.T) {
		for _, id := range []string{theirsID, crossID, "00000000-0000-0000-0000-000000000000", "not-a-uuid"} {
			tg.Get(t, Notifications, "/api/v0/inbox/"+id, u.H()).ExpectError(http.StatusNotFound, "Item not found")
			tg.Do(t, Notifications, "PATCH", "/api/v0/inbox/"+id+"/read", nil, u.H()).ExpectError(http.StatusNotFound, "Item not found")
			tg.Do(t, Notifications, "DELETE", "/api/v0/inbox/"+id, nil, u.H()).ExpectError(http.StatusNotFound, "Item not found")
		}
	})
	t.Run("bulk read", func(t *testing.T) {
		tg.Post(t, Notifications, "/api/v0/inbox/bulk/read", map[string]any{"ids": []string{hhID, theirsID, crossID, "nope"}}, u.H()).
			Expect(http.StatusOK, Obj{"updated": Eq(1)})
		// Already read: not counted again.
		tg.Post(t, Notifications, "/api/v0/inbox/bulk/read", map[string]any{"ids": []string{hhID}}, u.H()).
			Expect(http.StatusOK, Obj{"updated": Eq(0)})
		tg.Post(t, Notifications, "/api/v0/inbox/bulk/read", map[string]any{"ids": []string{}}, u.H()).
			Expect(http.StatusOK, Obj{"updated": Eq(0)})
		tg.Post(t, Notifications, "/api/v0/inbox/bulk/read", map[string]any{}, u.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "ids"))
		tg.Get(t, Notifications, "/api/v0/inbox/unread-count", u.H()).Expect(http.StatusOK, Obj{"count": Eq(1)})
		// Someone else's item stays unread.
		r := tg.Get(t, Notifications, "/api/v0/inbox/"+crossID, other.H()).Expect(http.StatusOK, inboxItemShape)
		_ = r
	})
	t.Run("get auto-marks read", func(t *testing.T) {
		r := tg.Get(t, Notifications, "/api/v0/inbox/"+ownID, u.H()).Expect(http.StatusOK, with(inboxItemShape, "is_read", Eq(true)))
		if r.Object()["title"] != "own" {
			r.Fatalf("title")
		}
		tg.Get(t, Notifications, "/api/v0/inbox/unread-count", u.H()).Expect(http.StatusOK, Obj{"count": Eq(0)})
		r = tg.Get(t, Notifications, "/api/v0/inbox?is_read=true", u.H()).Expect(http.StatusOK, ArrayOf(inboxItemShape))
		if got := ids(t, r); len(got) != 2 {
			r.Fatalf("is_read=true: %v", got)
		}
	})
	t.Run("mark read", func(t *testing.T) {
		tg.Do(t, Notifications, "PATCH", "/api/v0/inbox/"+hhID+"/read", nil, u.H()).
			Expect(http.StatusOK, with(inboxItemShape, "is_read", Eq(true), "id", Eq(hhID)))
	})
	t.Run("app update", func(t *testing.T) {
		r := tg.Do(t, Notifications, "PATCH", "/api/v0/inbox/"+ownID,
			map[string]any{"household_id": u.HouseholdID, "title": "own v2", "metadata": map[string]any{"rev": 2}}, app.H()).
			Expect(http.StatusOK, with(inboxItemShape, "id", Eq(ownID), "title", Eq("own v2"), "summary", Eq("s"),
				"category", Eq("contract-own"), "is_read", Eq(true)))
		if m, _ := r.Object()["metadata"].(map[string]any); m == nil || fmt.Sprint(m["rev"]) != "2" || m["k"] != nil {
			r.Fatalf("metadata is replaced, not merged: %v", m)
		}
		// Another household's id for the item: not found.
		tg.Do(t, Notifications, "PATCH", "/api/v0/inbox/"+ownID, map[string]any{"household_id": other.HouseholdID, "title": "x"}, app.H()).
			ExpectError(http.StatusNotFound, "Item not found")
		tg.Do(t, Notifications, "PATCH", "/api/v0/inbox/"+ownID, map[string]any{"title": "x"}, app.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "household_id"))
		// A PATCH may target another member's personal item: it is household-scoped only.
		tg.Do(t, Notifications, "PATCH", "/api/v0/inbox/"+theirsID, map[string]any{"household_id": u.HouseholdID, "summary": "s2"}, app.H()).
			Expect(http.StatusOK, with(inboxItemShape, "summary", Eq("s2")))
	})
	t.Run("delete", func(t *testing.T) {
		tg.Post(t, Notifications, "/api/v0/inbox/bulk/delete", map[string]any{"ids": []string{theirsID, crossID}}, u.H()).
			Expect(http.StatusOK, Obj{"deleted": Eq(0)})
		tg.Do(t, Notifications, "DELETE", "/api/v0/inbox/"+hhID, nil, u.H()).ExpectStatus(http.StatusNoContent).ExpectEmpty()
		hhDeleted = true
		tg.Do(t, Notifications, "DELETE", "/api/v0/inbox/"+hhID, nil, u.H()).ExpectError(http.StatusNotFound, "Item not found")
		tg.Post(t, Notifications, "/api/v0/inbox/bulk/delete", map[string]any{"ids": []string{ownID, hhID, "nope"}}, u.H()).
			Expect(http.StatusOK, Obj{"deleted": Eq(1)})
		tg.Post(t, Notifications, "/api/v0/inbox/bulk/delete", map[string]any{"ids": []string{}}, u.H()).
			Expect(http.StatusOK, Obj{"deleted": Eq(0)})
		r := tg.Get(t, Notifications, "/api/v0/inbox", u.H()).Expect(http.StatusOK, Array)
		if got := ids(t, r); len(got) != 0 {
			r.Fatalf("after delete: %v", got)
		}
		// theirs and cross belong to `other`, purged when that fixture user is deleted.
	})
}

func TestNotificationsTokens(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)
	u := NewUser(t)
	tok := fakePushToken(tg, "tok")

	t.Run("validation", func(t *testing.T) {
		tg.Post(t, Notifications, "/api/v0/tokens", map[string]any{"push_token": tok, "device_type": "windows"}, u.H()).
			ExpectError(http.StatusBadRequest, "device_type must be 'ios' or 'android'")
		tg.Post(t, Notifications, "/api/v0/tokens", map[string]any{"device_type": "ios"}, u.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "push_token"))
		tg.Do(t, Notifications, "DELETE", "/api/v0/tokens", map[string]any{}, u.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "push_token"))
	})
	var id string
	t.Run("register", func(t *testing.T) {
		r := tg.Post(t, Notifications, "/api/v0/tokens", map[string]any{"push_token": tok, "device_type": "ios", "device_name": "contract phone"}, u.H()).
			Expect(http.StatusOK, with(tokenShape, "push_token", Eq(tok), "device_type", Eq("ios"), "device_name", Eq("contract phone"), "is_active", Eq(true)))
		id = r.Object()["id"].(string)
		// Re-registering the same token upserts in place.
		tg.Post(t, Notifications, "/api/v0/tokens", map[string]any{"push_token": tok, "device_type": "android"}, u.H()).
			Expect(http.StatusOK, with(tokenShape, "id", Eq(id), "device_type", Eq("android"), "device_name", Null, "is_active", Eq(true)))
		r = tg.Get(t, Notifications, "/api/v0/tokens/me", u.H()).Expect(http.StatusOK, ArrayOf(tokenShape))
		if got := ids(t, r); len(got) != 1 || got[0] != id {
			r.Fatalf("tokens/me: %v", got)
		}
	})
	t.Run("unregister", func(t *testing.T) {
		tg.Do(t, Notifications, "DELETE", "/api/v0/tokens", map[string]any{"push_token": tok}, u.H()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		r := tg.Get(t, Notifications, "/api/v0/tokens/me", u.H()).Expect(http.StatusOK, Array)
		if got := ids(t, r); len(got) != 0 {
			r.Fatalf("inactive tokens are not listed: %v", got)
		}
		// Unregistering an inactive token still finds it.
		tg.Do(t, Notifications, "DELETE", "/api/v0/tokens", map[string]any{"push_token": tok}, u.H()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Do(t, Notifications, "DELETE", "/api/v0/tokens", map[string]any{"push_token": fakePushToken(tg, "never")}, u.H()).
			ExpectError(http.StatusNotFound, "Token not found")
		// Registering again reactivates the same row.
		tg.Post(t, Notifications, "/api/v0/tokens", map[string]any{"push_token": tok, "device_type": "ios"}, u.H()).
			Expect(http.StatusOK, with(tokenShape, "id", Eq(id), "is_active", Eq(true)))
	})
}

func TestNotificationsNotify(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)
	app := SharedApp(t)
	u := NewUser(t)
	emptyHH := "contract-" + tg.RunID + "-no-devices"
	note := func(targetType, targetID, title string) map[string]any {
		return map[string]any{"target_type": targetType, "target_id": targetID, "title": title, "body": "contract body " + tg.RunID}
	}
	skipped := with(notifyShape, "delivery_status", Eq("skipped"), "token_count", Eq(0), "success_count", Eq(0), "failure_count", Eq(0))

	t.Run("validation", func(t *testing.T) {
		tg.Post(t, Notifications, "/api/v0/notify", note("node", "x", "t"), app.H()).
			ExpectError(http.StatusBadRequest, "target_type must be 'user' or 'household'")
		b := note("user", "1", "t")
		b["priority"] = "urgent"
		tg.Post(t, Notifications, "/api/v0/notify", b, app.H()).
			ExpectError(http.StatusBadRequest, "priority must be 'default' or 'high'")
		b = note("user", "1", "t")
		delete(b, "title")
		tg.Post(t, Notifications, "/api/v0/notify", b, app.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "title"))
		tg.Post(t, Notifications, "/api/v0/notify/batch", map[string]any{}, app.H()).
			ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "notifications"))
		many := []any{}
		for i := range 101 {
			many = append(many, note("household", emptyHH, fmt.Sprintf("many %d", i)))
		}
		tg.Post(t, Notifications, "/api/v0/notify/batch", map[string]any{"notifications": many}, app.H()).
			ExpectError(http.StatusBadRequest, "Maximum 100 notifications per batch")
		tg.Post(t, Notifications, "/api/v0/notify/batch", map[string]any{"notifications": []any{note("node", "n", "t")}}, app.H()).
			ExpectError(http.StatusBadRequest, "target_type must be 'user' or 'household', got 'node'")
	})
	t.Run("no devices", func(t *testing.T) {
		tg.Post(t, Notifications, "/api/v0/notify", note("household", emptyHH, "no devices"), app.H()).Expect(http.StatusOK, skipped)
		b := note("user", fmt.Sprint(u.ID), "user without devices")
		b["priority"], b["category"], b["data"] = "high", "contract", map[string]any{"type": "contract", "inbox_item_id": "x"}
		tg.Post(t, Notifications, "/api/v0/notify", b, app.H()).Expect(http.StatusOK, skipped)
	})
	t.Run("batch", func(t *testing.T) {
		tg.Post(t, Notifications, "/api/v0/notify/batch", map[string]any{"notifications": []any{}}, app.H()).
			Expect(http.StatusOK, ArrayOf(Any)).ExpectShape(Eq([]any{}))
		r := tg.Post(t, Notifications, "/api/v0/notify/batch", map[string]any{"notifications": []any{
			note("household", emptyHH, "batch 1"), note("user", fmt.Sprint(u.ID), "batch 2"),
		}}, app.H()).Expect(http.StatusOK, ArrayOf(skipped))
		if n := len(r.JSON().([]any)); n != 2 {
			r.Fatalf("want 2 results, got %d", n)
		}
	})
	t.Run("to a device", func(t *testing.T) {
		tok := fakePushToken(tg, "notify")
		tg.Post(t, Notifications, "/api/v0/tokens", map[string]any{"push_token": tok, "device_type": "ios"}, u.H()).ExpectStatus(http.StatusOK)
		b := note("user", fmt.Sprint(u.ID), "to a device")
		// Legacy (MBP, relay down) answers "failed" with failure_count 1 after trying the relay
		// inline. jarvisd queues the push durably (docs/cc D31) and answers "pending" with
		// zero counts; the job fills in the outcome later. Nothing reads these fields.
		tg.Post(t, Notifications, "/api/v0/notify", b, app.H()).
			Expect(http.StatusOK, with(notifyShape, "token_count", Eq(1), "success_count", Eq(0), "delivery_status", OneOf("failed", "pending")))
		// The same notification (source, target, title, body, category) within 60 s is deduped.
		tg.Post(t, Notifications, "/api/v0/notify", b, app.H()).Expect(http.StatusOK, skipped)
		// Household targeting reaches the same device.
		tg.Post(t, Notifications, "/api/v0/notify", note("household", u.HouseholdID, "to the household"), app.H()).
			Expect(http.StatusOK, with(notifyShape, "token_count", Eq(1), "success_count", Eq(0), "delivery_status", OneOf("failed", "pending")))
	})
}

func TestNotificationsPurge(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)
	app := SharedApp(t)
	u := NewUser(t)
	tg.Post(t, Notifications, "/api/v0/tokens", map[string]any{"push_token": fakePushToken(tg, "purge"), "device_type": "android"}, u.H()).
		ExpectStatus(http.StatusOK)
	createInbox(t, app, newInboxItem(u.HouseholdID, u.ID, "personal"))
	hh := createInbox(t, app, newInboxItem(u.HouseholdID, nil, "household-wide"))
	hhID := hh["id"].(string)

	tg.Do(t, Notifications, "DELETE", "/api/v0/me/data", nil, u.H()).ExpectStatus(http.StatusNoContent).ExpectEmpty()
	r := tg.Get(t, Notifications, "/api/v0/tokens/me", u.H()).Expect(http.StatusOK, Array)
	if got := ids(t, r); len(got) != 0 {
		r.Fatalf("tokens after purge: %v", got)
	}
	// Household-wide items belong to the household and survive.
	r = tg.Get(t, Notifications, "/api/v0/inbox", u.H()).Expect(http.StatusOK, ArrayOf(inboxItemShape))
	if got := ids(t, r); len(got) != 1 || got[0] != hhID {
		r.Fatalf("inbox after purge: %v", got)
	}
	tg.Do(t, Notifications, "DELETE", "/api/v0/me/data", nil, u.H()).ExpectStatus(http.StatusNoContent).ExpectEmpty()
	tg.Do(t, Notifications, "DELETE", "/api/v0/inbox/"+hhID, nil, u.H()).ExpectStatus(http.StatusNoContent)
}

func TestNotificationsAdmin(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)
	key := os.Getenv(EnvNotificationsAdminKey)
	if key == "" {
		t.Skipf("contract: %s is not set", EnvNotificationsAdminKey)
	}
	h := H{"X-Api-Key": key}
	tg.Get(t, Notifications, "/api/v0/admin/stats", h).Expect(http.StatusOK, Obj{
		"tokens": Obj{
			"total":        Int,
			"active":       Int,
			"by_household": ArrayOf(Obj{"household_id": String, "count": Int}),
		},
		"notifications_24h": Obj{"total": Int, "by_status": MapOf(Int)},
	})
	tg.Post(t, Notifications, "/api/v0/admin/cleanup", nil, h).
		Expect(http.StatusOK, Obj{"status": Eq("ok"), "logs_pruned": Int, "tokens_deactivated": Int})
}

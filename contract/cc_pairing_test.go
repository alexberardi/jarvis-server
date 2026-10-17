//go:build contract

package contract

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Device-code pairing (docs/agent/DESIGN.md §13, docs/install/01-no-cli-plan.md item 1.1). New in
// jarvisd, so there is no Python oracle: these shapes freeze what jarvis-agent enroll, the
// admin's Nodes page and (Phase 3) the mobile app consume. Each run starts two pairings; the
// start limit is 10 per address per 10 minutes.

const pairingsPath = "/api/v0/nodes/pairings"

var pairingCode = Regexp(`^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`)

var pairingStarted = Obj{
	"pairing_id":  UUID,
	"poll_secret": NonEmptyString,
	"code":        pairingCode,
	"expires_at":  TimestampUTC,
	"expires_in":  Int,
	"interval":    Int,
}

var pairingPending = Obj{"status": Eq("pending"), "expires_in": Int, "interval": Int}

// The list never shows codes (loop #100): an approver types the one shown on the computer.
var pairingList = Obj{
	"pairings":   EqDeep([]any{}),
	"waiting":    Int,
	"households": NonEmptyArrayOf(Obj{"id": NonEmptyString, "name": String, "role": NonEmptyString}),
}

func pairingTarget(t *testing.T) *Target {
	t.Helper()
	tg := T(t)
	if !Jarvisd() {
		t.Skip("contract: device-code pairing is jarvisd-only (no legacy equivalent); set " + EnvImpl + "=jarvisd")
	}
	tg.NeedAdmin(t)
	tg.Need(t, CommandCenter, Auth)
	return tg
}

func TestCCPairing(t *testing.T) {
	tg := pairingTarget(t)
	u := SharedUser(t)
	outsider := NewUser(t) // a member of their own household only

	start := tg.Post(t, CommandCenter, pairingsPath, map[string]any{"name": "contract-pc-" + randHex(3), "platform": "linux"}).
		Expect(http.StatusCreated, pairingStarted).Object()
	id, secret, code := start["pairing_id"].(string), start["poll_secret"].(string), start["code"].(string)
	poll := func(sec string) *Resp {
		return tg.Post(t, CommandCenter, pairingsPath+"/"+id+"/poll", map[string]any{"poll_secret": sec})
	}

	t.Run("poll_pending", func(t *testing.T) {
		poll(secret).Expect(http.StatusOK, pairingPending)
		poll("wrong-"+randHex(4)).ExpectError(http.StatusNotFound, "Pairing not found or expired")
	})
	t.Run("list", func(t *testing.T) {
		tg.Get(t, CommandCenter, pairingsPath).ExpectError(http.StatusUnauthorized, "Missing or invalid Authorization header")
		l := tg.Get(t, CommandCenter, pairingsPath, u.H()).Expect(http.StatusOK, pairingList).Object()
		if n, _ := l["waiting"].(json.Number).Int64(); n < 1 {
			t.Fatalf("pairing %s not counted: %v", code, l)
		}
	})
	t.Run("cross_household_refused", func(t *testing.T) {
		tg.Post(t, CommandCenter, pairingsPath+"/approve", map[string]any{"code": code, "household_id": u.HouseholdID}, outsider.H()).
			ExpectError(http.StatusForbidden, "User is not a member of this household")
		poll(secret).Expect(http.StatusOK, pairingPending)
	})

	var reg map[string]any
	t.Run("approve_and_register", func(t *testing.T) {
		tg.Post(t, CommandCenter, pairingsPath+"/approve", map[string]any{"code": code, "household_id": u.HouseholdID, "room": "contract"}, u.H()).
			Expect(http.StatusOK, Obj{"status": Eq("approved"), "code": Eq(code), "household_id": Eq(u.HouseholdID), "room": Eq("contract"), "name": NonEmptyString})
		// Single use.
		tg.Post(t, CommandCenter, pairingsPath+"/approve", map[string]any{"code": code, "household_id": u.HouseholdID}, u.H()).
			ExpectError(http.StatusNotFound, "Pairing code not found or expired")
		cred := poll(secret).Expect(http.StatusOK, Obj{
			"status": Eq("approved"), "node_id": NonEmptyString, "provisioning_token": Regexp(`^prov_`),
			"household_id": Eq(u.HouseholdID), "household_name": String, "approved_by": String,
			"room": Eq("contract"), "name": NonEmptyString,
			"expires_at": TimestampNaive, "expires_in": Int,
			"node_command_center_url": Optional(NonEmptyString), "node_config_service_url": Optional(NonEmptyString),
		}).Object()
		reg = tg.Post(t, CommandCenter, "/api/v0/nodes/register", map[string]any{
			"node_id": cred["node_id"], "provisioning_token": cred["provisioning_token"],
		}).Expect(http.StatusCreated, Open{"node_id": Eq(cred["node_id"]), "room": Eq("contract"), "node_key": NonEmptyString}).Object()
		n := &CCNode{Node: Node{ID: reg["node_id"].(string), Key: reg["node_key"].(string), HouseholdID: u.HouseholdID}, Owner: u}
		t.Cleanup(func() {
			if err := tg.deleteCCNode(n); err != nil {
				t.Errorf("cleanup paired node: %v", err)
			}
		})
		poll(secret).ExpectError(http.StatusGone, "Pairing already used")
	})

	t.Run("deny", func(t *testing.T) {
		d := tg.Post(t, CommandCenter, pairingsPath, nil).Expect(http.StatusCreated, pairingStarted).Object()
		tg.Post(t, CommandCenter, pairingsPath+"/deny", map[string]any{"code": d["code"]}, u.H()).
			Expect(http.StatusOK, Obj{"status": Eq("denied"), "code": Eq(d["code"])})
		tg.Post(t, CommandCenter, pairingsPath+"/"+d["pairing_id"].(string)+"/poll", map[string]any{"poll_secret": d["poll_secret"]}).
			Expect(http.StatusOK, Obj{"status": Eq("denied")})
	})
}

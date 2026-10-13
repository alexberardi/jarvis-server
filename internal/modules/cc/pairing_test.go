package cc

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

const pairingsPath = "/api/v0/nodes/pairings"

type startedPairing struct{ id, secret, code string }

func (e *env) startPairing(name string) startedPairing {
	e.t.Helper()
	r := e.do("POST", pairingsPath, map[string]any{"name": name, "platform": "linux"}, nil).want(201).json()
	return startedPairing{id: r["pairing_id"].(string), secret: r["poll_secret"].(string), code: r["code"].(string)}
}

func (e *env) pollPairing(p startedPairing) *resp {
	e.t.Helper()
	return e.do("POST", pairingsPath+"/"+p.id+"/poll", map[string]any{"poll_secret": p.secret}, nil)
}

func approveBody(code, hh, room string) map[string]any {
	b := map[string]any{"code": code, "household_id": hh}
	if room != "" {
		b["room"] = room
	}
	return b
}

var codeRE = regexp.MustCompile(`^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`)

// The whole flow ends at /nodes/register, so the node is exactly one enrolled with a token.
func TestPairingHappyPath(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)

	start := e.do("POST", pairingsPath, map[string]any{"name": "Office-PC", "platform": "linux"}, nil).want(201).json()
	if !codeRE.MatchString(start["code"].(string)) || start["expires_in"] != 600.0 || start["interval"] != 3.0 {
		t.Fatalf("start: %v", start)
	}
	if len(start["poll_secret"].(string)) < 40 {
		t.Fatalf("poll secret too short: %v", start)
	}
	p := startedPairing{id: start["pairing_id"].(string), secret: start["poll_secret"].(string), code: start["code"].(string)}
	if got := e.pollPairing(p).want(200).json(); got["status"] != "pending" {
		t.Fatalf("poll: %v", got)
	}

	// The member sees the same code, and the households they can approve into.
	list := e.do("GET", pairingsPath, nil, bearer(member)).want(200).json()
	ps := list["pairings"].([]any)
	if len(ps) != 1 {
		t.Fatalf("list: %v", list)
	}
	row := ps[0].(map[string]any)
	if row["code"] != p.code || row["name"] != "Office-PC" || row["platform"] != "linux" || row["requested_from"] != "127.0.0.1" {
		t.Fatalf("row: %v", row)
	}
	for _, k := range []string{"pairing_id", "poll_secret", "provisioning_token"} {
		if _, ok := row[k]; ok {
			t.Fatalf("list leaks %s: %v", k, row)
		}
	}
	hs := list["households"].([]any)
	if len(hs) != 1 || hs[0].(map[string]any)["id"] != "hh1" {
		t.Fatalf("households: %v", hs)
	}

	ap := e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh1", "office"), bearer(member)).want(200).json()
	if ap["household_id"] != "hh1" || ap["room"] != "office" || ap["name"] != "Office-PC" {
		t.Fatalf("approve: %v", ap)
	}
	if _, ok := ap["provisioning_token"]; ok {
		t.Fatalf("approval leaks the token: %v", ap)
	}
	// Gone from the waiting list.
	if got := e.do("GET", pairingsPath, nil, bearer(member)).want(200).json()["pairings"].([]any); len(got) != 0 {
		t.Fatalf("still listed: %v", got)
	}

	cred := e.pollPairing(p).want(200).json()
	if cred["status"] != "approved" || cred["household_id"] != "hh1" || cred["room"] != "office" ||
		!strings.HasPrefix(cred["provisioning_token"].(string), "prov_") || cred["node_id"] == "" {
		t.Fatalf("approved poll: %v", cred)
	}
	reg := e.do("POST", "/api/v0/nodes/register", map[string]any{
		"node_id": cred["node_id"], "provisioning_token": cred["provisioning_token"],
	}, nil).want(201).json()
	if reg["room"] != "office" || reg["user"] != "default" || reg["voice_mode"] != "brief" || reg["node_key"] == "" {
		t.Fatalf("register: %v", reg)
	}
	nid := cred["node_id"].(string)
	row2, err := e.m.nodeByID(t.Context(), nid)
	if err != nil || row2.householdID.String != "hh1" || row2.room != "office" {
		t.Fatalf("node row: %+v %v", row2, err)
	}
	if !e.auth.active(nid) {
		t.Fatal("auth has no active node")
	}
	// The token row carries the approver, as a member-minted token does.
	var by int64
	if err := e.d.Read.QueryRow(`SELECT created_by_user_id FROM cc_provisioning_tokens WHERE node_id = ?`, nid).Scan(&by); err != nil || by != 1 {
		t.Fatalf("created_by %d %v", by, err)
	}
	// Done: the pairing can't hand out anything more.
	e.pollPairing(p).detail(410, "Pairing already used")
	e.pollPairing(p).detail(404, pairingNotFound)
}

// A lost answer: polling again replaces the token, so only the latest one works.
func TestPairingRepollReplacesToken(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	p := e.startPairing("pc")
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh1", ""), bearer(member)).want(200)
	first := e.pollPairing(p).want(200).json()
	second := e.pollPairing(p).want(200).json()
	if first["node_id"] != second["node_id"] || first["provisioning_token"] == second["provisioning_token"] {
		t.Fatalf("re-poll: %v / %v", first, second)
	}
	if second["room"] != pairingRoom {
		t.Fatalf("default room: %v", second)
	}
	e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": first["node_id"], "provisioning_token": first["provisioning_token"]}, nil).
		detail(401, "Invalid or expired provisioning token")
	e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": second["node_id"], "provisioning_token": second["provisioning_token"]}, nil).want(201)
}

func TestPairingExpiry(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	p := e.startPairing("pc")
	e.advance(pairingTTL - time.Second)
	if got := e.pollPairing(p).want(200).json(); got["status"] != "pending" || got["expires_in"] != 1.0 {
		t.Fatalf("before expiry: %v", got)
	}
	e.advance(time.Second)
	e.pollPairing(p).detail(404, pairingNotFound)
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh1", ""), bearer(member)).detail(404, "Pairing code not found or expired")
	if got := e.do("GET", pairingsPath, nil, bearer(member)).want(200).json()["pairings"].([]any); len(got) != 0 {
		t.Fatalf("expired still listed: %v", got)
	}
}

func TestPairingCodeSingleUse(t *testing.T) {
	e := newEnv(t)
	a := e.auth.addUser(1, "hh1", authn.RoleMember)
	b := e.auth.addUser(2, "hh2", authn.RoleMember)
	p := e.startPairing("pc")
	// Typed the way a person might: lower case, no dash.
	typed := strings.ToLower(strings.ReplaceAll(p.code, "-", ""))
	e.do("POST", pairingsPath+"/approve", approveBody(typed, "hh1", ""), bearer(a)).want(200)
	// The code is spent: no second approval (by anyone, into anywhere), no denial.
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh1", ""), bearer(a)).detail(404, "Pairing code not found or expired")
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh2", ""), bearer(b)).detail(404, "Pairing code not found or expired")
	e.do("POST", pairingsPath+"/deny", map[string]any{"code": p.code}, bearer(a)).detail(404, "Pairing code not found or expired")
	if got := e.pollPairing(p).want(200).json(); got["household_id"] != "hh1" {
		t.Fatalf("approval changed: %v", got)
	}

	// Denied: the requester hears it once, then the pairing is gone.
	d := e.startPairing("other")
	e.do("POST", pairingsPath+"/deny", map[string]any{"code": d.code}, bearer(b)).want(200)
	e.do("POST", pairingsPath+"/approve", approveBody(d.code, "hh2", ""), bearer(b)).detail(404, "Pairing code not found or expired")
	if got := e.pollPairing(d).want(200).json(); got["status"] != "denied" || got["provisioning_token"] != nil {
		t.Fatalf("denied poll: %v", got)
	}
	e.pollPairing(d).detail(404, pairingNotFound)
}

// The approver picks one of their OWN households: another household is refused, superuser or
// not, and the pairing stays pending for a real member.
func TestPairingCrossHouseholdRefused(t *testing.T) {
	e := newEnv(t)
	a := e.auth.addUser(1, "hh1", authn.RoleMember)
	owner2 := e.auth.addUser(2, "hh2", authn.RoleOwner)
	super := e.auth.addUser(3, "hh3", authn.RoleOwner)
	e.auth.mu.Lock()
	u := e.auth.users[super]
	u.IsSuperuser = true
	e.auth.users[super] = u
	e.auth.mu.Unlock()

	p := e.startPairing("pc")
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh2", ""), bearer(a)).detail(403, "User is not a member of this household")
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh2", ""), bearer(super)).detail(403, "User is not a member of this household")
	if got := e.pollPairing(p).want(200).json(); got["status"] != "pending" {
		t.Fatalf("after refused approvals: %v", got)
	}
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh2", "den"), bearer(owner2)).want(200)
	if got := e.pollPairing(p).want(200).json(); got["household_id"] != "hh2" || got["room"] != "den" {
		t.Fatalf("approved: %v", got)
	}
}

// Someone in no household can't see, approve or deny pairings; nobody unauthenticated can.
func TestPairingNonMemberRefused(t *testing.T) {
	e := newEnv(t)
	loner := e.auth.addUser(5, "", "")
	p := e.startPairing("pc")
	e.do("GET", pairingsPath, nil, bearer(loner)).detail(403, "User is not a member of any household")
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh1", ""), bearer(loner)).detail(403, "User is not a member of this household")
	e.do("POST", pairingsPath+"/deny", map[string]any{"code": p.code}, bearer(loner)).detail(403, "User is not a member of any household")
	e.do("GET", pairingsPath, nil, nil).detail(401, "Missing or invalid Authorization header")
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh1", ""), nil).detail(401, "Missing or invalid Authorization header")
	e.do("POST", pairingsPath+"/deny", map[string]any{"code": p.code}, bearer("junk")).detail(401, "Invalid token")
	if got := e.pollPairing(p).want(200).json(); got["status"] != "pending" {
		t.Fatalf("pairing changed: %v", got)
	}
}

// Only the requester, holding the poll secret, can ever read the credentials.
func TestPairingPollNeedsSecret(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	p := e.startPairing("pc")
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh1", ""), bearer(member)).want(200)
	for _, bad := range []startedPairing{
		{id: p.id, secret: "wrong"},
		{id: p.id, secret: ""},
		{id: "no-such-id", secret: p.secret},
	} {
		e.pollPairing(bad).detail(404, pairingNotFound)
	}
	// A member's token is not a poll secret.
	e.do("POST", pairingsPath+"/"+p.id+"/poll", map[string]any{"poll_secret": member}, bearer(member)).detail(404, pairingNotFound)
	e.do("POST", pairingsPath+"/"+p.id+"/poll", map[string]any{}, nil).want(400)
	if got := e.pollPairing(p).want(200).json(); got["status"] != "approved" {
		t.Fatalf("the real secret: %v", got)
	}
}

func TestPairingStartRateLimit(t *testing.T) {
	e := newEnv(t)
	for i := range pairingStartLimit {
		e.do("POST", pairingsPath, map[string]any{"name": fmt.Sprint("pc", i)}, nil).want(201)
	}
	r := e.do("POST", pairingsPath, nil, nil)
	r.detail(429, "Too many pairing requests from this address. Try again later.")
	if r.header.Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	e.advance(pairingTTL)
	e.do("POST", pairingsPath, nil, nil).want(201)
}

func TestPairingPendingCap(t *testing.T) {
	e := newEnv(t)
	s := e.m.pairings
	s.mu.Lock()
	for i := range maxPendingPairings {
		p := &pairing{id: fmt.Sprint("fill-", i), code: fmt.Sprintf("FILL%04d", i), expires: e.clock().Add(pairingTTL)}
		s.byID[p.id], s.byCode[p.code] = p, p
	}
	s.mu.Unlock()
	e.do("POST", pairingsPath, nil, nil).detail(429, "Too many pairings waiting for approval. Try again later.")
}

// Wrong codes and refused households count against the user; once locked out, even the right
// code is refused until the window passes.
func TestPairingApproveRateLimit(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	other := e.auth.addUser(2, "hh1", authn.RoleMember)
	p := e.startPairing("pc")
	e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh-not-mine", ""), bearer(member)).want(403)
	for i := 1; i < pairingApproveFailLimit; i++ {
		e.do("POST", pairingsPath+"/approve", approveBody(fmt.Sprintf("ZZZZ-%04d", i), "hh1", ""), bearer(member)).want(404)
	}
	r := e.do("POST", pairingsPath+"/approve", approveBody(p.code, "hh1", ""), bearer(member))
	r.detail(429, "Too many failed pairing approvals. Try again later.")
	if r.header.Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	e.do("POST", pairingsPath+"/deny", map[string]any{"code": p.code}, bearer(member)).want(429)
	// Per user: another member is unaffected.
	if got := e.do("GET", pairingsPath, nil, bearer(other)).want(200).json()["pairings"].([]any); len(got) != 1 {
		t.Fatalf("list: %v", got)
	}
	e.advance(pairingTTL / 2)
	q := e.startPairing("pc2")
	e.advance(pairingTTL / 2)
	e.do("POST", pairingsPath+"/approve", approveBody(q.code, "hh1", ""), bearer(member)).want(200)
}

func TestPairingStartValidation(t *testing.T) {
	e := newEnv(t)
	e.do("POST", pairingsPath, map[string]any{"name": 5}, nil).want(400)
	long := strings.Repeat("x", 200) + "\x07"
	r := e.do("POST", pairingsPath, map[string]any{"name": long}, nil).want(201).json()
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	got := e.do("GET", pairingsPath, nil, bearer(member)).want(200).json()["pairings"].([]any)[0].(map[string]any)
	if got["name"] != strings.Repeat("x", 64) || got["code"] != r["code"] {
		t.Fatalf("cleaned name: %v", got)
	}
	// No body at all: a default name.
	e.do("POST", pairingsPath, nil, nil).want(201)
}

package legacyimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Service grants every imported node gets (backfilled when legacy lacks them): the node's
// HTTP and MQTT login (cc.ServiceName) and its log shipping (logs.ServiceID).
var RequiredNodeGrants = []string{"jarvis-command-center", "jarvis-logs"}

// Options control Run.
type Options struct {
	// Apply commits the import; otherwise everything is rolled back (a dry run).
	Apply bool
	// AcceptHeads lets a legacy database at another alembic head through (db key → head).
	AcceptHeads map[string]string
	// Settings are the jarvisd modules' setting definitions (module → definitions). A legacy
	// settings row is imported only when its key is defined here, and its value parses and
	// validates. The <module>_settings tables must exist.
	Settings map[string][]settings.Definition
	// PromptProvider validates llm.prompt_provider (renamed from llm.interface); nil accepts any.
	PromptProvider func(name string) error
	// Now is the clock for "unexpired" (default time.Now).
	Now func() time.Time
}

// settingsBlocked are defined in jarvisd but never carried over.
var settingsBlocked = map[string]string{
	// Legacy seeded HS256; jarvisd mints RS256 and HS256 needs the legacy shared secret.
	"auth.algorithm": "jarvisd chooses the JWT algorithm (RS256)",
}

// settingsSkippedDBs are legacy settings tables not carried over at all.
var settingsSkippedDBs = map[string]string{
	"llm": "legacy model/engine settings don't apply to jarvisd's engines; models are chosen in the setup wizard",
}

// settingRenames map legacy keys to jarvisd's, per module.
var settingRenames = map[string]map[string]string{
	"cc": {"llm.interface": "llm.prompt_provider"}, // D11
}

// freshTables must be empty in the target: the import keeps legacy ids.
var freshTables = []string{
	"auth_users", "auth_households", "auth_household_memberships", "auth_node_registrations",
	"auth_node_service_access", "cc_nodes", "cc_rooms", "cc_devices", "cc_user_memories",
	"cc_routines", "cc_schedules", "cc_phone_contacts", "cc_phone_call_sessions", "cc_signals",
	"cc_proposal_suppressions", "notifications_inbox_items",
}

// ErrRefused is returned (with the report) when the import refused; the report says why.
var ErrRefused = errors.New("import-legacy refused; nothing was written")

type importer struct {
	ctx   context.Context
	tx    *sql.Tx
	src   Source
	opt   Options
	rep   *Report
	now   time.Time
	users map[int64]bool
	hhs   map[string]bool
	// regs maps an imported node registration to its household.
	regs     map[string]string
	rooms    map[string]bool
	cnodes   map[string]bool
	contacts map[string]bool
}

// Run imports src into d. It checks the legacy heads and that d is fresh, then inserts every
// row inside one transaction, each under a savepoint so one rejected row is reported without
// hiding the others. Any rejected row, a dry run, or a refusal rolls everything back. The
// report is returned even with an error.
func Run(ctx context.Context, d *db.DB, src Source, opt Options) (*Report, error) {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	rep := &Report{Source: src.Describe(), Apply: opt.Apply, Heads: map[string]string{}}
	present := map[string]bool{}
	for _, l := range LegacyDBs {
		head, ok, err := src.Head(ctx, l.Key)
		if err != nil {
			return rep, err
		}
		present[l.Key] = ok
		rep.Heads[l.Key] = head
		switch {
		case !ok && l.Required:
			rep.Refusals = append(rep.Refusals, fmt.Sprintf("legacy database %s (%s) is missing", l.Key, l.DefaultName))
		case !ok:
			rep.notef("legacy database %s (%s) is absent: nothing from it", l.Key, l.DefaultName)
		case l.Pin != "" && head != l.Pin && opt.AcceptHeads[l.Key] != head:
			rep.Refusals = append(rep.Refusals, fmt.Sprintf(
				"legacy %s schema is at alembic head %q, not %s which this importer reads; check the column mapping, then --accept-head %s=%s",
				l.Key, head, l.Pin, l.Key, head))
		}
	}
	if !rep.OK() {
		return rep, ErrRefused
	}

	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return rep, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var busy []string
	for _, t := range freshTables {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
			return rep, fmt.Errorf("target %s: %w (is the database migrated?)", t, err)
		}
		if n > 0 {
			busy = append(busy, fmt.Sprintf("%s (%d)", t, n))
		}
	}
	if len(busy) > 0 {
		rep.Refusals = append(rep.Refusals, "the jarvisd database is not fresh: "+strings.Join(busy, ", ")+
			" already hold rows. import-legacy runs once, on a new install before the setup wizard's account step")
		return rep, ErrRefused
	}

	im := &importer{ctx: ctx, tx: tx, src: src, opt: opt, rep: rep, now: opt.Now().UTC(),
		users: map[int64]bool{}, hhs: map[string]bool{}, regs: map[string]string{}, rooms: map[string]bool{},
		cnodes: map[string]bool{}, contacts: map[string]bool{}}
	steps := []func() error{
		im.users_, im.households, im.memberships, im.nodeRegistrations, im.nodeGrants,
		im.ccRooms, im.ccNodes, im.ccDevices, im.ccMemories, im.ccRoutines, im.ccSchedules,
		im.ccContacts, im.ccCalls, im.ccSignals, im.ccSuppressions, im.inbox,
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return rep, err
		}
	}
	for _, l := range LegacyDBs {
		if l.Module != "" && present[l.Key] {
			if err := im.settingsFrom(l); err != nil {
				return rep, err
			}
		}
	}
	if len(rep.Superusers) == 0 {
		rep.notef("no active superuser imported: setup stays open and the wizard's Account step creates one")
	}
	if len(rep.Errors) > 0 {
		rep.Refusals = append(rep.Refusals, fmt.Sprintf("%d rows don't fit jarvisd's schema (listed above); fix them on the legacy side or report a mapping bug", len(rep.Errors)))
		return rep, ErrRefused
	}
	if !opt.Apply {
		return rep, nil
	}
	if err := tx.Commit(); err != nil {
		return rep, err
	}
	committed = true
	rep.Committed = true
	return rep, nil
}

// rows fetches a legacy table; an absent table is noted and yields nothing.
func (im *importer) rows(q Query) ([]Row, error) {
	rows, ok, err := im.src.Rows(im.ctx, q)
	if err != nil {
		return nil, err
	}
	if !ok {
		im.rep.notef("legacy %s.%s is absent: nothing from it", q.DB, q.Table)
	}
	return rows, nil
}

// insert writes one row under a savepoint; a rejection is recorded and rolled back alone.
func (im *importer) insert(t *TableReport, key string, c *conv, cols []string, vals []any) bool {
	if c != nil && c.err != nil {
		im.reject(t, key, c.err)
		return false
	}
	if _, err := im.tx.ExecContext(im.ctx, "SAVEPOINT legacy_row"); err != nil {
		im.reject(t, key, err)
		return false
	}
	q := "INSERT INTO " + t.Table + " (" + quoteCols(cols) + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
	if _, err := im.tx.ExecContext(im.ctx, q, vals...); err != nil {
		_, _ = im.tx.ExecContext(im.ctx, "ROLLBACK TO legacy_row")
		_, _ = im.tx.ExecContext(im.ctx, "RELEASE legacy_row")
		im.reject(t, key, err)
		return false
	}
	if _, err := im.tx.ExecContext(im.ctx, "RELEASE legacy_row"); err != nil {
		im.reject(t, key, err)
		return false
	}
	t.Imported++
	im.rep.logf("%s %s: imported", t.Table, key)
	return true
}

func (im *importer) reject(t *TableReport, key string, err error) {
	t.Failed++
	im.rep.Errors = append(im.rep.Errors, fmt.Sprintf("%s %s: %v", t.Table, key, err))
	im.rep.logf("%s %s: REJECTED: %v", t.Table, key, err)
}

func (im *importer) skip(t *TableReport, key, reason string) {
	t.skip(reason)
	im.rep.logf("%s %s: skipped: %s", t.Table, key, reason)
}

func quoteCols(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = `"` + c + `"`
	}
	return strings.Join(q, ", ")
}

// knownUser maps a nullable user reference: ok is false when it names a user not imported.
func (im *importer) knownUser(c *conv, k string) (any, bool) {
	id, ok := c.id(k)
	if !ok {
		return nil, true
	}
	return id, im.users[id]
}

// orUser keeps a user reference or nulls it when the user wasn't imported.
func (im *importer) orUser(t *TableReport, c *conv, k string) any {
	v, ok := im.knownUser(c, k)
	if !ok {
		t.fix(k + " of a user not imported → NULL")
		return nil
	}
	return v
}

func (im *importer) expired(c *conv, k string) bool {
	t, ok := c.time(k)
	return ok && !t.After(im.now)
}

// --- auth ---

func (im *importer) users_() error {
	q := Query{DB: "auth", Table: "users", Order: "id", Columns: []string{"id", "email", "username", "password_hash",
		"is_active", "is_superuser", "must_change_password", "temp_password_expires_at", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("auth_users", "auth.users")
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: usTime}
		id, _ := c.id("id")
		key := "id=" + strconv.FormatInt(id, 10)
		created := c.TOr("created_at", im.now.Format(usTime))
		ok := im.insert(t, key, c, q.Columns, []any{c.N("id"), c.S("email"), c.S("username"), c.S("password_hash"),
			c.B("is_active", 1), c.B("is_superuser", 0), c.B("must_change_password", 0), c.T("temp_password_expires_at"),
			created, c.TOr("updated_at", created)})
		if ok {
			im.users[id] = true
			im.rep.logf("auth_users %s: email %s", key, c.str("email"))
			if c.truthy("is_superuser", false) && c.truthy("is_active", true) {
				im.rep.Superusers = append(im.rep.Superusers, fmt.Sprintf("id %d (%s)", id, MaskEmail(c.str("email"))))
			}
		}
	}
	return nil
}

func (im *importer) households() error {
	q := Query{DB: "auth", Table: "households", Order: "created_at, id", Columns: []string{"id", "name", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("auth_households", "auth.households")
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: usTime}
		id := c.str("id")
		created := c.TOr("created_at", im.now.Format(usTime))
		if im.insert(t, "id="+id, c, q.Columns, []any{id, c.S("name"), created, c.TOr("updated_at", created)}) {
			im.hhs[id] = true
		}
	}
	return nil
}

func (im *importer) memberships() error {
	q := Query{DB: "auth", Table: "household_memberships", Order: "id", Columns: []string{"id", "household_id", "user_id", "role", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("auth_household_memberships", "auth.household_memberships")
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: usTime}
		id, _ := c.id("id")
		key := "id=" + strconv.FormatInt(id, 10)
		uid, _ := c.id("user_id")
		if !im.hhs[c.str("household_id")] {
			im.skip(t, key, "household not imported")
			continue
		}
		if !im.users[uid] {
			im.skip(t, key, "user not imported")
			continue
		}
		role := c.str("role")
		if l := strings.ToLower(role); l != role {
			t.fix("role lower-cased (" + role + " → " + l + ")")
			role = l
		}
		created := c.TOr("created_at", im.now.Format(usTime))
		im.insert(t, key, c, q.Columns, []any{id, c.S("household_id"), uid, role, created, c.TOr("updated_at", created)})
	}
	return nil
}

func (im *importer) nodeRegistrations() error {
	q := Query{DB: "auth", Table: "node_registrations", Order: "id", Columns: []string{"id", "node_id", "node_key_hash", "name",
		"is_active", "household_id", "registered_by_user_id", "created_at", "updated_at", "last_rotated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("auth_node_registrations", "auth.node_registrations")
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: usTime}
		node := c.str("node_id")
		key := "node_id=" + node
		hh := c.str("household_id")
		if !im.hhs[hh] {
			im.skip(t, key, "household not imported")
			continue
		}
		created := c.TOr("created_at", im.now.Format(usTime))
		active := c.B("is_active", 1)
		if im.insert(t, key, c, q.Columns, []any{c.N("id"), node, c.S("node_key_hash"), c.S("name"), active, hh,
			im.orUser(t, c, "registered_by_user_id"), created, c.TOr("updated_at", created), c.T("last_rotated_at")}) {
			im.regs[node] = hh
			if active == 0 {
				im.rep.InactiveNodes = append(im.rep.InactiveNodes, node)
			}
		}
	}
	return nil
}

func (im *importer) nodeGrants() error {
	q := Query{DB: "auth", Table: "node_service_access", Order: "id", Columns: []string{"id", "node_id", "service_id", "granted_at", "granted_by"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("auth_node_service_access", "auth.node_service_access")
	have := map[string]bool{}
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: usTime}
		node, svc := c.str("node_id"), c.str("service_id")
		key := "node_id=" + node + " service=" + svc
		if _, ok := im.regs[node]; !ok {
			im.skip(t, key, "node not imported")
			continue
		}
		if im.insert(t, key, c, q.Columns, []any{c.N("id"), node, svc, c.TOr("granted_at", im.now.Format(usTime)), im.orUser(t, c, "granted_by")}) {
			have[node+"\x00"+svc] = true
		}
	}
	nodes := make([]string, 0, len(im.regs))
	for n := range im.regs {
		nodes = append(nodes, n)
	}
	slices.Sort(nodes)
	for _, n := range nodes {
		for _, svc := range RequiredNodeGrants {
			if have[n+"\x00"+svc] {
				continue
			}
			key := "node_id=" + n + " service=" + svc
			if im.insert(t, key, nil, []string{"node_id", "service_id", "granted_at"}, []any{n, svc, im.now.Format(usTime)}) {
				t.Imported-- // counted as a fix-up, not a legacy row
				t.fix("grant " + svc + " backfilled")
				im.rep.logf("auth_node_service_access %s: backfilled", key)
			}
		}
	}
	return nil
}

// --- cc ---

func (im *importer) ccRooms() error {
	q := Query{DB: "cc", Table: "rooms", Order: "id", Columns: []string{"id", "household_id", "name", "normalized_name", "icon",
		"ha_area_id", "parent_room_id", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_rooms", "cc.rooms")
	byID := map[string]Row{}
	var pending []Row
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		key := "id=" + c.str("id")
		if !im.hhs[c.str("household_id")] {
			im.skip(t, key, "household not imported")
			continue
		}
		byID[c.str("id")] = r
		pending = append(pending, r)
	}
	// Parents before children (parent_room_id references cc_rooms).
	for len(pending) > 0 {
		var next []Row
		progressed := false
		for _, r := range pending {
			c := &conv{r: r, layout: msTime}
			parent := c.str("parent_room_id")
			if parent != "" && byID[parent] != nil && !im.rooms[parent] && parent != c.str("id") {
				next = append(next, r)
				continue
			}
			progressed = true
			var pv any
			if parent != "" {
				if im.rooms[parent] {
					pv = parent
				} else {
					t.fix("parent_room_id of a room not imported → NULL")
				}
			}
			id := c.str("id")
			if im.insert(t, "id="+id, c, q.Columns, []any{id, c.S("household_id"), c.S("name"), c.S("normalized_name"),
				c.S("icon"), c.S("ha_area_id"), pv, c.T("created_at"), c.T("updated_at")}) {
				im.rooms[id] = true
			}
			byID[id] = nil // done (imported or rejected)
		}
		if !progressed { // a parent cycle: break it
			for _, r := range next {
				r["parent_room_id"] = nil
				t.fix("parent_room_id in a cycle → NULL")
			}
		}
		pending = next
	}
	return nil
}

func (im *importer) ccNodes() error {
	q := Query{DB: "cc", Table: "nodes", Order: "node_id", Columns: []string{"node_id", "room", "user", "voice_mode", "last_seen",
		"room_id", "household_id", "last_seen_version", "install_mode", "is_busy", "git_sha", "is_active", "protocols", "needs_k2"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_nodes", "cc.nodes")
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		node := c.str("node_id")
		key := "node_id=" + node
		hh, registered := im.regs[node]
		if !registered {
			im.skip(t, key, "no imported auth registration (it can't log in)")
			continue
		}
		switch cur := c.str("household_id"); {
		case cur == "":
			t.fix("household_id filled from the auth registration")
		case cur != hh:
			t.fix("household_id replaced by the auth registration's")
		}
		var room any
		if rid := c.str("room_id"); rid != "" {
			if im.rooms[rid] {
				room = rid
			} else {
				t.fix("room_id of a room not imported → NULL")
			}
		}
		if c.truthy("is_busy", false) {
			t.fix("is_busy cleared")
		}
		if im.insert(t, key, c, q.Columns, []any{node, c.S("room"), c.S("user"), c.S("voice_mode"), c.T("last_seen"),
			room, hh, c.S("last_seen_version"), c.S("install_mode"), 0, c.S("git_sha"), c.B("is_active", 1),
			c.S("protocols"), c.B("needs_k2", 1)}) {
			im.cnodes[node] = true
		}
	}
	var missing []string
	for n := range im.regs {
		if !im.cnodes[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		im.rep.notef("registered nodes with no cc_nodes row (command-center answers 401 \"Node not configured locally\", as legacy did): %s",
			strings.Join(missing, ", "))
	}
	return nil
}

func (im *importer) ccDevices() error {
	q := Query{DB: "cc", Table: "devices", Order: "id", Columns: []string{"id", "household_id", "room_id", "entity_id", "name",
		"domain", "device_class", "manufacturer", "model", "source", "ha_device_id", "is_controllable", "is_active",
		"protocol", "local_ip", "mac_address", "cloud_id", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_devices", "cc.devices")
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		key := "id=" + c.str("id")
		if !im.hhs[c.str("household_id")] {
			im.skip(t, key, "household not imported")
			continue
		}
		var room any
		if rid := c.str("room_id"); rid != "" {
			if im.rooms[rid] {
				room = rid
			} else {
				t.fix("room_id of a room not imported → NULL")
			}
		}
		im.insert(t, key, c, q.Columns, []any{c.S("id"), c.S("household_id"), room, c.S("entity_id"), c.S("name"),
			c.S("domain"), c.S("device_class"), c.S("manufacturer"), c.S("model"), c.S("source"), c.S("ha_device_id"),
			c.B("is_controllable", nil), c.B("is_active", nil), c.S("protocol"), c.S("local_ip"), c.S("mac_address"),
			c.S("cloud_id"), c.T("created_at"), c.T("updated_at")})
	}
	return nil
}

func (im *importer) ccMemories() error {
	q := Query{DB: "cc", Table: "user_memories", Order: "id", Columns: []string{"id", "user_id", "household_id", "category", "key",
		"content", "source", "is_active", "is_pinned", "created_at", "updated_at", "expires_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_user_memories", "cc.user_memories")
	now := im.now.Format(msTime)
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		id, _ := c.id("id")
		key := "id=" + strconv.FormatInt(id, 10)
		uid, userOK := im.knownUser(c, "user_id")
		switch {
		case !c.truthy("is_active", true):
			im.skip(t, key, "forgotten (is_active = false)")
		case im.expired(c, "expires_at"):
			im.skip(t, key, "expired")
		case !im.hhs[c.str("household_id")]:
			im.skip(t, key, "household not imported")
		case !userOK:
			im.skip(t, key, "user not imported")
		default:
			// The embedding stays NULL: the memory sweep embeds it with jarvisd's model.
			created := c.TOr("created_at", now)
			im.insert(t, key, c, q.Columns, []any{id, uid, c.S("household_id"), c.S("category"), c.S("key"),
				c.S("content"), c.S("source"), 1, c.B("is_pinned", 0), created, c.TOr("updated_at", created), c.T("expires_at")})
		}
	}
	return nil
}

func (im *importer) ccRoutines() error {
	q := Query{DB: "cc", Table: "routines", Order: "created_at, id", Columns: []string{"id", "household_id", "slug", "name",
		"trigger_phrases", "steps", "response_instruction", "response_length", "schedule", "enabled", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_routines", "cc.routines")
	now := im.now.Format(msTime)
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		key := "id=" + c.str("id")
		if !im.hhs[c.str("household_id")] {
			im.skip(t, key, "household not imported")
			continue
		}
		created := c.TOr("created_at", now)
		im.insert(t, key, c, q.Columns, []any{c.S("id"), c.S("household_id"), c.S("slug"), c.S("name"),
			c.S("trigger_phrases"), c.S("steps"), c.S("response_instruction"), c.S("response_length"), c.S("schedule"),
			c.B("enabled", 1), created, c.TOr("updated_at", created)})
	}
	return nil
}

func (im *importer) ccSchedules() error {
	q := Query{DB: "cc", Table: "schedules", Order: "created_at, id", Columns: []string{"id", "household_id", "user_id", "node_id",
		"intent", "timezone", "next_fire_at", "recurrence", "state", "last_fired_at", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_schedules", "cc.schedules")
	now := im.now.Format(msTime)
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		key := "id=" + c.str("id")
		uid, userOK := im.knownUser(c, "user_id")
		switch {
		case c.str("state") != "active":
			im.skip(t, key, "state "+c.str("state")+" (only active schedules carry over)")
		case !im.hhs[c.str("household_id")]:
			im.skip(t, key, "household not imported")
		case !userOK:
			im.skip(t, key, "user not imported")
		default:
			created := c.TOr("created_at", now)
			im.insert(t, key, c, q.Columns, []any{c.S("id"), c.S("household_id"), uid, c.S("node_id"), c.S("intent"),
				c.S("timezone"), c.T("next_fire_at"), c.S("recurrence"), "active", c.T("last_fired_at"), created,
				c.TOr("updated_at", created)})
		}
	}
	return nil
}

func (im *importer) ccContacts() error {
	q := Query{DB: "cc", Table: "phone_contacts", Order: "created_at, id", Columns: []string{"id", "household_id", "name",
		"normalized_name", "number", "address", "source", "line_type", "do_not_call", "notes", "verified_at", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_phone_contacts", "cc.phone_contacts")
	now := im.now.Format(msTime)
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		key := "id=" + c.str("id")
		if !im.hhs[c.str("household_id")] {
			im.skip(t, key, "household not imported")
			continue
		}
		src := c.S("source")
		if src == "web" {
			src = "manual"
			t.fix("source web → manual")
		}
		created := c.TOr("created_at", now)
		if im.insert(t, key, c, q.Columns, []any{c.S("id"), c.S("household_id"), c.S("name"), c.S("normalized_name"),
			c.S("number"), c.S("address"), src, c.S("line_type"), c.B("do_not_call", 0), c.S("notes"), c.T("verified_at"),
			created, c.TOr("updated_at", created)}) {
			im.contacts[c.str("id")] = true
		}
	}
	return nil
}

var terminalCallStates = []string{"done", "failed", "declined", "expired"}

func (im *importer) ccCalls() error {
	q := Query{DB: "cc", Table: "phone_call_sessions", Order: "created_at, id", Columns: []string{"id", "household_id", "user_id",
		"confirmed_by", "contact_id", "contact_name", "contact_address", "goal", "details", "resolved_number", "dialed_number",
		"number_edited", "line_type", "state", "error_message", "transcript_json", "outcome_json", "audio_object_key",
		"worker_url", "heartbeat_at", "twilio_call_sid", "duration_seconds", "errand_id", "errand_step", "created_at",
		"confirmed_at", "expires_at", "ended_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_phone_call_sessions", "cc.phone_call_sessions")
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		key := "id=" + c.str("id")
		state := c.str("state")
		if !slices.Contains(terminalCallStates, state) {
			im.skip(t, key, "state "+state+" (only finished calls carry over)")
			continue
		}
		if !im.hhs[c.str("household_id")] {
			im.skip(t, key, "household not imported")
			continue
		}
		var contact any
		if cid := c.str("contact_id"); cid != "" {
			if im.contacts[cid] {
				contact = cid
			} else {
				t.fix("contact_id of a contact not imported → NULL")
			}
		}
		im.insert(t, key, c, q.Columns, []any{c.S("id"), c.S("household_id"), im.orUser(t, c, "user_id"),
			im.orUser(t, c, "confirmed_by"), contact, c.S("contact_name"), c.S("contact_address"), c.S("goal"), c.S("details"),
			c.S("resolved_number"), c.S("dialed_number"), c.B("number_edited", 0), c.S("line_type"), state,
			c.S("error_message"), c.S("transcript_json"), c.S("outcome_json"), c.S("audio_object_key"), c.S("worker_url"),
			c.T("heartbeat_at"), c.S("twilio_call_sid"), c.N("duration_seconds"), c.S("errand_id"), c.N("errand_step"),
			c.TOr("created_at", im.now.Format(msTime)), c.T("confirmed_at"), c.T("expires_at"), c.T("ended_at")})
	}
	return nil
}

func (im *importer) ccSignals() error {
	q := Query{DB: "cc", Table: "signals", Order: "id", Columns: []string{"id", "household_id", "user_id", "node_id", "room", "kind",
		"subject", "source_key", "summary", "facts", "source_agent", "cacheable", "salience", "observed_at", "expires_at",
		"is_active", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_signals", "cc.signals")
	now := im.now.Format(msTime)
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		id, _ := c.id("id")
		key := "id=" + strconv.FormatInt(id, 10)
		uid, userOK := im.knownUser(c, "user_id")
		switch {
		case im.expired(c, "expires_at"):
			im.skip(t, key, "expired")
		case !im.hhs[c.str("household_id")]:
			im.skip(t, key, "household not imported")
		case !userOK:
			im.skip(t, key, "user not imported")
		default:
			created := c.TOr("created_at", now)
			im.insert(t, key, c, q.Columns, []any{id, c.S("household_id"), uid, c.S("node_id"), c.S("room"), c.S("kind"),
				c.S("subject"), c.S("source_key"), c.S("summary"), c.S("facts"), c.S("source_agent"), c.B("cacheable", 0),
				c.N("salience"), c.T("observed_at"), c.T("expires_at"), c.B("is_active", 1), created, c.TOr("updated_at", created)})
		}
	}
	return nil
}

func (im *importer) ccSuppressions() error {
	q := Query{DB: "cc", Table: "proposal_suppressions", Order: "created_at, id", Columns: []string{"id", "household_id", "user_id",
		"command", "source_key", "descriptor", "created_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("cc_proposal_suppressions", "cc.proposal_suppressions")
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		key := "id=" + c.str("id")
		uid, userOK := im.knownUser(c, "user_id")
		switch {
		case !im.hhs[c.str("household_id")]:
			im.skip(t, key, "household not imported")
		case !userOK:
			im.skip(t, key, "user not imported")
		default:
			// Keys are copied byte-for-byte (doc 10 §11: idempotency keys must match).
			im.insert(t, key, c, q.Columns, []any{c.S("id"), c.S("household_id"), uid, c.S("command"), c.S("source_key"),
				c.S("descriptor"), c.TOr("created_at", im.now.Format(msTime))})
		}
	}
	return nil
}

// --- notifications ---

func (im *importer) inbox() error {
	q := Query{DB: "notifications", Table: "inbox_items", Order: "created_at, id", Columns: []string{"id", "user_id", "household_id",
		"title", "summary", "body", "category", "source_service", "metadata_json", "is_read", "created_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table("notifications_inbox_items", "notifications.inbox_items")
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: usTime}
		key := "id=" + c.str("id")
		uid, userOK := im.knownUser(c, "user_id")
		switch {
		case c.str("category") == "adapter_proposal":
			im.skip(t, key, "adapter_proposal (the LoRA pipeline is cut)")
		case !im.hhs[c.str("household_id")]:
			im.skip(t, key, "household not imported")
		case !userOK:
			im.skip(t, key, "user not imported")
		default:
			im.insert(t, key, c, q.Columns, []any{c.S("id"), uid, c.S("household_id"), c.S("title"), c.S("summary"),
				c.S("body"), c.S("category"), c.S("source_service"), c.S("metadata_json"), c.B("is_read", 0),
				c.TOr("created_at", im.now.Format(usTime))})
		}
	}
	return nil
}

// --- settings ---

type settingRow struct {
	c       *conv
	key     string
	scope   string
	updated time.Time
	id      int64
}

func (im *importer) settingsFrom(l LegacyDB) error {
	q := Query{DB: l.Key, Table: "settings", Order: "id", Columns: []string{"id", "key", "value", "value_type",
		"household_id", "node_id", "user_id", "created_at", "updated_at"}}
	rows, err := im.rows(q)
	if err != nil {
		return err
	}
	t := im.rep.table(l.Module+"_settings", l.Key+".settings")
	if why, skipped := settingsSkippedDBs[l.Key]; skipped {
		for _, r := range rows {
			t.Read++
			c := &conv{r: r, layout: msTime}
			im.skip(t, "key="+c.str("key"), why)
		}
		return nil
	}
	defs := map[string]settings.Definition{}
	for _, d := range im.opt.Settings[l.Module] {
		defs[d.Key] = d
	}
	// Keep one row per (key, scope): the most recently updated (Postgres let duplicate
	// system-scope rows through; jarvisd's unique index doesn't).
	var keep []*settingRow
	best := map[string]*settingRow{}
	for _, r := range rows {
		t.Read++
		c := &conv{r: r, layout: msTime}
		id, _ := c.id("id")
		k := c.str("key")
		if nk, ok := settingRenames[l.Module][k]; ok {
			t.fix(k + " renamed " + nk)
			k = nk
		}
		uid, userOK := im.knownUser(c, "user_id")
		hh, node := c.str("household_id"), c.str("node_id")
		label := fmt.Sprintf("id=%d key=%s", id, k)
		def, defined := defs[k]
		system := hh == "" && node == "" && uid == nil
		switch {
		case settingsBlocked[k] != "":
			im.skip(t, label, settingsBlocked[k])
			continue
		case !defined:
			im.skip(t, label, "not a jarvisd setting (cut or renamed)")
			continue
		case system && !im.changed(c):
			im.skip(t, label, "legacy seeded default, never changed (jarvisd's own default applies)")
			continue
		case hh != "" && !im.hhs[hh]:
			im.skip(t, label, "household not imported")
			continue
		case !userOK:
			im.skip(t, label, "user not imported")
			continue
		}
		if err := im.checkValue(k, def, c.r["value"]); err != nil {
			im.skip(t, label, "value not valid for jarvisd ("+err.Error()+")")
			continue
		}
		upd, ok := c.time("updated_at")
		if !ok {
			upd, _ = c.time("created_at")
		}
		sr := &settingRow{c: c, key: k, scope: fmt.Sprintf("%s\x00%s\x00%v", hh, node, uid), updated: upd, id: id}
		dk := k + "\x00" + sr.scope
		if prev := best[dk]; prev != nil {
			if sr.updated.After(prev.updated) || (sr.updated.Equal(prev.updated) && sr.id > prev.id) {
				im.skip(t, fmt.Sprintf("id=%d key=%s", prev.id, k), "duplicate of a newer row for the same scope")
				best[dk] = sr
				*prev = *sr
			} else {
				im.skip(t, label, "duplicate of a newer row for the same scope")
			}
			continue
		}
		best[dk] = sr
		keep = append(keep, sr)
	}
	for _, sr := range keep {
		c, def := sr.c, defs[sr.key]
		label := fmt.Sprintf("id=%d key=%s", sr.id, sr.key)
		uid, _ := im.knownUser(c, "user_id")
		var hh, node any
		if v := c.str("household_id"); v != "" {
			hh = v
		}
		if v := c.str("node_id"); v != "" {
			node = v
		}
		var exists int
		if err := im.tx.QueryRowContext(im.ctx, "SELECT COUNT(*) FROM "+t.Table+` WHERE key = ? AND COALESCE(household_id, '') = COALESCE(?, '')
			AND COALESCE(node_id, '') = COALESCE(?, '') AND COALESCE(user_id, 0) = COALESCE(?, 0)`, sr.key, hh, node, uid).Scan(&exists); err != nil {
			return fmt.Errorf("%s: %w (is the settings table migrated?)", t.Table, err)
		}
		if exists > 0 {
			im.skip(t, label, "already set in jarvisd")
			continue
		}
		var envFallback any
		if def.EnvFallback != "" {
			envFallback = def.EnvFallback
		}
		created := c.TOr("created_at", im.now.Format(msTime))
		im.insert(t, label, c, []string{"key", "value", "value_type", "category", "description", "requires_reload", "is_secret",
			"env_fallback", "household_id", "node_id", "user_id", "created_at", "updated_at"},
			[]any{sr.key, c.S("value"), string(def.Type), def.Category, def.Description, boolInt(def.RequiresReload),
				boolInt(def.IsSecret), envFallback, hh, node, uid, created, c.TOr("updated_at", created)})
	}
	return nil
}

// changed reports whether a legacy settings row was written after it was seeded: legacy
// services seed every definition at startup (updated_at NULL, or equal to created_at), so a
// later updated_at is a value someone chose.
func (im *importer) changed(c *conv) bool {
	upd, ok := c.time("updated_at")
	if !ok {
		return false
	}
	created, ok := c.time("created_at")
	return !ok || upd.Sub(created) > time.Second
}

// checkValue parses a stored value as the definition's type and runs its validator.
func (im *importer) checkValue(key string, def settings.Definition, raw any) error {
	if raw == nil {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return errors.New("not text")
	}
	if s == "" {
		return nil
	}
	var v any
	var err error
	switch def.Type {
	case settings.Int:
		v, err = strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	case settings.Float:
		v, err = strconv.ParseFloat(strings.TrimSpace(s), 64)
	case settings.Bool:
		switch strings.ToLower(s) {
		case "true", "1", "yes", "on":
			v = true
		case "false", "0", "no", "off":
			v = false
		default:
			err = errors.New("not a boolean")
		}
	case settings.JSON:
		err = json.Unmarshal([]byte(s), &v)
	default:
		v = s
	}
	if err != nil {
		return fmt.Errorf("not a %s", def.Type)
	}
	if key == "llm.prompt_provider" && im.opt.PromptProvider != nil {
		if err := im.opt.PromptProvider(s); err != nil {
			return errors.New("unknown prompt provider")
		}
	}
	if def.Validate != nil {
		if err := def.Validate(v); err != nil {
			return errors.New("rejected by its validator")
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

package recipes

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	pathpkg "path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// The cutover import (docs/recipes/00-inventory.md §13, R11; RD6): recipes (+ ingredients,
// steps, tags), meal plans (+ items), staples and SKU mappings from a legacy export bundle,
// owned by the jarvisd accounts that match the legacy users by email. The rules are those of
// recipes PR #39's scripts/remap_users.py, carried over from an in-place rewrite to an import:
//
//   - Users match by email, case-insensitively. An unmatched author's rows are not imported and
//     are reported; a later run, after they sign up, imports them (incremental). With
//     ParkUnmatched, their household-shared rows are imported now under the author
//     "legacy-<id>" (owned by nobody: no jarvisd id is ever written for someone who has no
//     account, so no current or future account can inherit their rows), and a later run gives
//     them to the account once it exists. Their private rows (no household) always wait.
//   - A legacy household maps to a household of its highest-ranking matched member (admin,
//     power_user, member; membership order breaks ties); several → the one with the same name,
//     then the one every matched member shares; still ambiguous is reported (Households
//     settles it by hand). A NULL household stays NULL (the author's private rows).
//   - Refused, nothing written: two legacy users matching one jarvisd account, two legacy
//     households mapping to one jarvisd household (counting earlier runs), and an override
//     naming a household jarvisd does not have.
//   - recipes_import_log makes it idempotent: a logged row is never imported again (even when
//     it was deleted in jarvisd since), and the user and household mappings a run settled are
//     kept by later runs.
//   - Legacy integer ids are kept where free (plan items and the app refer to recipe ids);
//     otherwise SQLite allocates one and the references are rewritten within the import.
//   - Transforms: source_type lowercased; naive timestamps are UTC; quantity_value to REAL
//     (4 decimals); tags merge by name (a new one keeps its legacy id when free); a `/media/<name>` photo is copied from the bundle into
//     the blob store with the URL unchanged (RD3); an absolute URL to the legacy server's
//     /media becomes relative; a photo the bundle lacks, or any other non-http URL, is dropped
//     (null) and reported.
//   - Staples and SKU mappings that already exist in the target scope are matched, not duplicated.
//
// Everything is one SQLite transaction; a dry run (the default) runs it and rolls it back, so
// the report is exactly what --apply would do, and copies no photos.

// Accounts is jarvisd's view of its users and households (from the auth tables).
type Accounts struct {
	Emails         map[int64]string  // user id → email
	HouseholdNames map[string]string // household id → name
	Memberships    []Membership      // oldest first
}

// Membership is one jarvisd household membership.
type Membership struct {
	HouseholdID string
	UserID      int64
	Role        string
}

// ImportOptions steers Import.
type ImportOptions struct {
	Apply         bool
	Households    map[string]string // legacy household id → jarvisd household id (by hand)
	ParkUnmatched bool
	// LegacyHosts are the legacy recipes server's host[:port] names; an absolute image URL to
	// one of them at /media/<name> is the editor photo <name>. A URL whose /media/<name> the
	// bundle has is recognised without this.
	LegacyHosts []string
	Now         func() time.Time
}

// ImportReport is what a run did, or (dry run) would do.
type ImportReport struct {
	Applied            bool
	Source             string
	ExportedAt         string
	Refusals           []string
	MatchedUsers       []UserMatch
	UnmatchedUsers     []UserMiss
	Households         []HouseholdMatch
	UnmappedHouseholds []HouseholdMiss
	Counts             map[string]*ImportCount // by kind
	Skipped            []string                // one line per row left out, with why
	Reowned            int                     // parked rows given to their author's account
	Photos             PhotoCounts
	PhotoNotes         []string
}

// UserMatch is a legacy user with a jarvisd account.
type UserMatch struct {
	Legacy  string
	New     int64
	Email   string
	Earlier bool // settled by an earlier run
}

// UserMiss is a legacy author without one.
type UserMiss struct {
	Legacy string
	Email  string
	Reason string
}

// HouseholdMatch is a mapped legacy household.
type HouseholdMatch struct {
	Legacy, New, Name string
	How               string // "owner", "override", "earlier run"
}

// HouseholdMiss is a legacy household that could not be mapped.
type HouseholdMiss struct {
	Legacy, Reason string
}

// ImportCount counts one kind of row.
type ImportCount struct {
	Imported  int // written now
	Matched   int // already in jarvisd (an identical staple or mapping): logged, not written
	Already   int // imported by an earlier run
	Skipped   int // left for a later run, or not importable
	IDChanged int // imported under a new id because the legacy one was taken
}

// PhotoCounts counts editor photos.
type PhotoCounts struct {
	Copied, Present, Dropped, Relativised int
}

// ImportKinds is the report's row order.
var ImportKinds = []string{"recipe", "ingredient", "step", "recipe_tag", "meal_plan", "meal_plan_item", "staple", "sku_map"}

// parkPrefix marks the author of a row whose legacy author had no jarvisd account (--park-unmatched).
const parkPrefix = "legacy-"

var errDryRun = errors.New("dry run")

var roleRank = map[string]int{"admin": 0, "power_user": 1, "member": 2}

type importer struct {
	ctx  context.Context
	tx   *sql.Tx
	b    *Bundle
	acc  Accounts
	opt  ImportOptions
	blob blob.Store
	rep  *ImportReport
	now  string

	log      map[string]map[string]string // kind → legacy id → new id
	users    map[string]int64             // legacy user id → jarvisd user id
	userMiss map[string]string            // legacy user id → reason
	houses   map[string]string            // legacy household → jarvisd household
	houseMis map[string]string            // legacy household → reason

	maxLegacy map[string]int64 // table → highest legacy id in the bundle
	nextID    map[string]int64 // table → next id for a row whose legacy id is taken

	recipeIDs map[int64]int64 // legacy recipe → jarvisd recipe (this run and earlier ones)
	planIDs   map[int64]int64
	photos    map[string]bool // photo names handled this run
}

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// MaskEmail shows an email as a***@domain (reports may be pasted into chats and logs).
func MaskEmail(e string) string {
	local, domain, ok := strings.Cut(strings.TrimSpace(e), "@")
	if !ok || local == "" {
		return "***"
	}
	r := []rune(local)
	return string(r[0]) + "***@" + domain
}

func idSortKey(a, b string) bool {
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	if (aerr == nil) != (berr == nil) {
		return aerr == nil
	}
	return a < b
}

// Import runs the cutover import of b into d (and blobs) for the accounts acc.
func Import(ctx context.Context, d *db.DB, blobs blob.Store, b *Bundle, acc Accounts, opt ImportOptions) (*ImportReport, error) {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	rep := &ImportReport{Source: b.Manifest.Source, ExportedAt: b.Manifest.ExportedAt, Counts: map[string]*ImportCount{}}
	for _, k := range ImportKinds {
		rep.Counts[k] = &ImportCount{}
	}
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		im := &importer{
			ctx: ctx, tx: tx, b: b, acc: acc, opt: opt, blob: blobs, rep: rep, now: ts(opt.Now()),
			users: map[string]int64{}, userMiss: map[string]string{},
			houses: map[string]string{}, houseMis: map[string]string{},
			recipeIDs: map[int64]int64{}, planIDs: map[int64]int64{}, photos: map[string]bool{},
			maxLegacy: bundleMaxIDs(b), nextID: map[string]int64{},
		}
		if err := im.loadLog(); err != nil {
			return err
		}
		im.planUsers()
		im.planHouseholds()
		if len(rep.Refusals) > 0 {
			return errDryRun // nothing written
		}
		if err := im.logMappings(); err != nil {
			return err
		}
		for _, step := range []func() error{im.reown, im.recipes, im.plans, im.planItems, im.staples, im.skuMap} {
			if err := step(); err != nil {
				return err
			}
		}
		if !opt.Apply {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	} else if err == nil {
		rep.Applied = true
	}
	if err != nil {
		return nil, err
	}
	return rep, nil
}

func (im *importer) loadLog() error {
	im.log = map[string]map[string]string{}
	rows, err := im.tx.QueryContext(im.ctx, `SELECT legacy_kind, legacy_id, new_id FROM recipes_import_log`)
	if err != nil {
		return fmt.Errorf("recipes_import_log (is jarvisd's recipes migration 00002 applied?): %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, l, n string
		if err := rows.Scan(&k, &l, &n); err != nil {
			return err
		}
		if im.log[k] == nil {
			im.log[k] = map[string]string{}
		}
		im.log[k][l] = n
	}
	return rows.Err()
}

func (im *importer) logged(kind, legacy string) (string, bool) {
	n, ok := im.log[kind][legacy]
	return n, ok
}

func (im *importer) writeLog(kind, legacy, newID string) error {
	if im.log[kind] == nil {
		im.log[kind] = map[string]string{}
	}
	im.log[kind][legacy] = newID
	_, err := im.tx.ExecContext(im.ctx, `INSERT INTO recipes_import_log (legacy_kind, legacy_id, new_id, imported_at)
		VALUES (?, ?, ?, ?) ON CONFLICT (legacy_kind, legacy_id) DO UPDATE SET new_id = excluded.new_id`, kind, legacy, newID, im.now)
	return err
}

// --- users and households ---

func (im *importer) authors() []string {
	set := map[string]bool{}
	for _, r := range im.b.Recipes {
		set[string(r.UserID)] = true
	}
	for _, p := range im.b.MealPlans {
		set[string(p.UserID)] = true
	}
	for _, s := range im.b.Staples {
		set[string(s.UserID)] = true
	}
	for _, s := range im.b.SkuMap {
		set[string(s.UserID)] = true
	}
	// Authors parked by an earlier run, whose rows a match now re-owns.
	for l := range im.log["parked"] {
		set[l] = true
	}
	out := make([]string, 0, len(set))
	for u := range set {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return idSortKey(out[i], out[j]) })
	return out
}

func (im *importer) planUsers() {
	legacyEmail := map[string]string{}
	for _, u := range im.b.AuthUsers {
		legacyEmail[string(u.ID)] = u.Email
	}
	byEmail := map[string]int64{}
	dupEmail := map[string]bool{}
	for id, e := range im.acc.Emails {
		n := normEmail(e)
		if prev, ok := byEmail[n]; ok && prev != id {
			dupEmail[n] = true
		}
		byEmail[n] = id
	}
	// jarvisd account → the legacy user an earlier run gave it.
	earlierOwner := map[int64]string{}
	for l, n := range im.log["user"] {
		if id, err := strconv.ParseInt(n, 10, 64); err == nil {
			earlierOwner[id] = l
		}
	}
	claimed := map[int64]string{}
	for _, l := range im.authors() {
		email := legacyEmail[l]
		if n, ok := im.logged("user", l); ok {
			if id, err := strconv.ParseInt(n, 10, 64); err == nil {
				if _, exists := im.acc.Emails[id]; exists {
					im.match(l, id, email, true, claimed)
					continue
				}
			}
		}
		if email == "" {
			im.miss(l, "", "no legacy account with this id")
			continue
		}
		n := normEmail(email)
		if dupEmail[n] {
			im.miss(l, email, "several jarvisd accounts have this email")
			continue
		}
		id, ok := byEmail[n]
		if !ok {
			im.miss(l, email, "no jarvisd account with this email yet")
			continue
		}
		if prev, ok := earlierOwner[id]; ok && prev != l {
			im.rep.Refusals = append(im.rep.Refusals, fmt.Sprintf(
				"jarvisd user %d (%s) already received legacy user %s's rows in an earlier run; legacy user %s matches it too",
				id, MaskEmail(email), prev, l))
			continue
		}
		im.match(l, id, email, false, claimed)
	}
}

func (im *importer) match(l string, id int64, email string, earlier bool, claimed map[int64]string) {
	if prev, ok := claimed[id]; ok {
		im.rep.Refusals = append(im.rep.Refusals, fmt.Sprintf(
			"legacy users %s and %s both match jarvisd user %d (%s); refusing to merge them", prev, l, id, MaskEmail(email)))
		return
	}
	claimed[id] = l
	im.users[l] = id
	if email == "" {
		email = im.acc.Emails[id]
	}
	im.rep.MatchedUsers = append(im.rep.MatchedUsers, UserMatch{Legacy: l, New: id, Email: email, Earlier: earlier})
}

func (im *importer) miss(l, email, reason string) {
	im.userMiss[l] = reason
	im.rep.UnmatchedUsers = append(im.rep.UnmatchedUsers, UserMiss{Legacy: l, Email: email, Reason: reason})
}

func (im *importer) referencedHouseholds() []string {
	set := map[string]bool{}
	add := func(h *string) {
		if h != nil && *h != "" {
			set[*h] = true
		}
	}
	for _, r := range im.b.Recipes {
		add(r.HouseholdID)
	}
	for _, p := range im.b.MealPlans {
		add(p.HouseholdID)
	}
	for _, s := range im.b.Staples {
		add(s.HouseholdID)
	}
	for _, s := range im.b.SkuMap {
		add(s.HouseholdID)
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func (im *importer) planHouseholds() {
	legacyName := map[string]string{}
	for _, h := range im.b.AuthHouses {
		legacyName[string(h.ID)] = h.Name
	}
	type member struct{ user, role string }
	members := map[string][]member{}
	for _, m := range im.b.AuthMembers {
		members[string(m.HouseholdID)] = append(members[string(m.HouseholdID)], member{string(m.UserID), strings.ToLower(m.Role)})
	}
	userHouses := map[int64][]string{}
	for _, m := range im.acc.Memberships {
		userHouses[m.UserID] = append(userHouses[m.UserID], m.HouseholdID)
	}
	earlier := map[string]string{} // jarvisd household → legacy household (earlier runs)
	for l, n := range im.log["household"] {
		earlier[n] = l
	}
	for _, old := range im.referencedHouseholds() {
		name := legacyName[old]
		if nh, ok := im.opt.Households[old]; ok {
			if _, exists := im.acc.HouseholdNames[nh]; !exists {
				im.rep.Refusals = append(im.rep.Refusals, fmt.Sprintf("--household %s=%s: jarvisd has no such household", old, nh))
				continue
			}
			im.mapHouse(old, nh, "override")
			continue
		}
		if nh, ok := im.logged("household", old); ok {
			if _, exists := im.acc.HouseholdNames[nh]; exists {
				im.mapHouse(old, nh, "earlier run")
				continue
			}
		}
		if _, ok := legacyName[old]; !ok {
			im.unmapped(old, "no legacy household with this id")
			continue
		}
		var ranked []member
		for _, m := range members[old] {
			if _, ok := im.users[m.user]; ok {
				ranked = append(ranked, m)
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return rank(ranked[i].role) < rank(ranked[j].role) })
		if len(ranked) == 0 {
			im.unmapped(old, fmt.Sprintf("%q: no member of it has a jarvisd account yet", name))
			continue
		}
		owner := im.users[ranked[0].user]
		cands := userHouses[owner]
		if len(cands) > 1 {
			var same []string
			for _, c := range cands {
				if strings.EqualFold(strings.TrimSpace(im.acc.HouseholdNames[c]), strings.TrimSpace(name)) {
					same = append(same, c)
				}
			}
			if len(same) == 1 {
				cands = same
			} else {
				shared := map[string]int{}
				for _, m := range ranked {
					for _, h := range userHouses[im.users[m.user]] {
						shared[h]++
					}
				}
				var all []string
				for _, c := range cands {
					if shared[c] == len(ranked) {
						all = append(all, c)
					}
				}
				if len(all) == 1 {
					cands = all
				}
			}
		}
		switch len(cands) {
		case 0:
			im.unmapped(old, fmt.Sprintf("%q: its owner (jarvisd user %d) is in no jarvisd household", name, owner))
		case 1:
			im.mapHouse(old, cands[0], "owner")
		default:
			var opts []string
			for _, c := range cands {
				opts = append(opts, fmt.Sprintf("%s (%s)", c, im.acc.HouseholdNames[c]))
			}
			im.unmapped(old, fmt.Sprintf("%q is ambiguous: jarvisd user %d is in %s; settle it with --household %s=<id>",
				name, owner, strings.Join(opts, ", "), old))
		}
	}
	// Two legacy households into one jarvisd household, now or counting earlier runs.
	into := map[string]string{}
	olds := make([]string, 0, len(im.houses))
	for old := range im.houses {
		olds = append(olds, old)
	}
	sort.Strings(olds)
	for _, old := range olds {
		nh := im.houses[old]
		if prev, ok := into[nh]; ok {
			im.rep.Refusals = append(im.rep.Refusals, fmt.Sprintf(
				"legacy households %s and %s both map to jarvisd household %s; refusing to merge them", prev, old, nh))
			continue
		}
		if prev, ok := earlier[nh]; ok && prev != old {
			im.rep.Refusals = append(im.rep.Refusals, fmt.Sprintf(
				"jarvisd household %s already received legacy household %s in an earlier run; legacy household %s maps to it too", nh, prev, old))
			continue
		}
		into[nh] = old
	}
}

func rank(role string) int {
	if r, ok := roleRank[role]; ok {
		return r
	}
	return 9
}

func (im *importer) mapHouse(old, nh, how string) {
	im.houses[old] = nh
	im.rep.Households = append(im.rep.Households, HouseholdMatch{Legacy: old, New: nh, Name: im.acc.HouseholdNames[nh], How: how})
}

func (im *importer) unmapped(old, reason string) {
	im.houseMis[old] = reason
	im.rep.UnmappedHouseholds = append(im.rep.UnmappedHouseholds, HouseholdMiss{Legacy: old, Reason: reason})
}

func (im *importer) logMappings() error {
	for _, m := range im.rep.MatchedUsers {
		if err := im.writeLog("user", m.Legacy, strconv.FormatInt(m.New, 10)); err != nil {
			return err
		}
	}
	for _, h := range im.rep.Households {
		if err := im.writeLog("household", h.Legacy, h.New); err != nil {
			return err
		}
	}
	return nil
}

// owner decides a row's author and household, or why it is left out.
func (im *importer) owner(legacyUser flexID, hh *string) (uid string, hid any, parked bool, why string) {
	l := string(legacyUser)
	id, matched := im.users[l]
	if hh == nil || *hh == "" {
		if matched {
			return strconv.FormatInt(id, 10), nil, false, ""
		}
		return "", nil, false, fmt.Sprintf("private row of legacy user %s (%s): imported once they sign up", l, im.userMiss[l])
	}
	nh, ok := im.houses[*hh]
	if !ok {
		reason := im.houseMis[*hh]
		if reason == "" {
			reason = "not mapped"
		}
		return "", nil, false, fmt.Sprintf("household %s: %s", *hh, reason)
	}
	if matched {
		return strconv.FormatInt(id, 10), nh, false, ""
	}
	if im.opt.ParkUnmatched {
		return parkPrefix + l, nh, true, ""
	}
	return "", nil, false, fmt.Sprintf("author legacy user %s (%s): imported once they sign up, or now with --park-unmatched", l, im.userMiss[l])
}

func (im *importer) skip(kind, id, why string) {
	im.rep.Counts[kind].Skipped++
	im.rep.Skipped = append(im.rep.Skipped, fmt.Sprintf("%s %s: %s", kind, id, why))
}

// bundleMaxIDs is the highest legacy id per target table.
func bundleMaxIDs(b *Bundle) map[string]int64 {
	m := map[string]int64{}
	up := func(table string, id int64) {
		if id > m[table] {
			m[table] = id
		}
	}
	for _, r := range b.Recipes {
		up("recipes_recipes", r.ID)
	}
	for _, i := range b.Ingredients {
		up("recipes_ingredients", i.ID)
	}
	for _, t := range b.Tags {
		up("recipes_tags", t.ID)
	}
	for _, s := range b.Steps {
		up("recipes_steps", s.ID)
	}
	for _, p := range b.MealPlans {
		up("recipes_meal_plans", p.ID)
	}
	for _, i := range b.PlanItems {
		up("recipes_meal_plan_items", i.ID)
	}
	for _, s := range b.Staples {
		up("recipes_staples", s.ID)
	}
	for _, s := range b.SkuMap {
		up("recipes_grocery_sku_map", s.ID)
	}
	return m
}

// rowID is the id a legacy row gets: its own when free, else one above every id the table
// has and every legacy id the bundle holds, so a moved row never takes a later row's id (which
// would move that one too).
func (im *importer) rowID(table string, legacyID int64) (int64, error) {
	var one int
	err := im.tx.QueryRowContext(im.ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, legacyID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return legacyID, nil
	}
	if err != nil {
		return 0, err
	}
	if im.nextID[table] == 0 {
		var maxID int64
		if err := im.tx.QueryRowContext(im.ctx, `SELECT COALESCE(MAX(id), 0) FROM `+table).Scan(&maxID); err != nil {
			return 0, err
		}
		im.nextID[table] = max(maxID, im.maxLegacy[table]) + 1
	}
	id := im.nextID[table]
	im.nextID[table]++
	return id, nil
}

func (im *importer) insert(kind, table string, legacyID int64, cols string, args ...any) (int64, error) {
	newID, err := im.rowID(table, legacyID)
	if err != nil {
		return 0, err
	}
	ph := strings.Repeat(", ?", len(args))
	if _, err := im.tx.ExecContext(im.ctx, `INSERT INTO `+table+` (id, `+cols+`) VALUES (?`+ph+`)`, append([]any{newID}, args...)...); err != nil {
		return 0, fmt.Errorf("%s %d: %w", kind, legacyID, err)
	}
	c := im.rep.Counts[kind]
	c.Imported++
	if newID != legacyID {
		c.IDChanged++
	}
	return newID, nil
}

// legacyTS is a legacy timestamp as jarvisd stores it; naive values are UTC. nil → fallback.
func legacyTS(s *string, fallback string) any {
	if s == nil || *s == "" {
		if fallback == "" {
			return nil
		}
		return fallback
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999Z07", "2006-01-02 15:04:05.999999999Z07"} {
		if t, err := time.Parse(layout, *s); err == nil {
			return ts(t)
		}
	}
	return *s
}

// --- re-owning parked rows ---

func (im *importer) reown() error {
	for l := range im.log["parked"] {
		id, ok := im.users[l]
		if !ok {
			continue
		}
		uid, parked := strconv.FormatInt(id, 10), parkPrefix+l
		for _, t := range []string{"recipes_recipes", "recipes_meal_plans", "recipes_staples", "recipes_grocery_sku_map"} {
			res, err := im.tx.ExecContext(im.ctx, `UPDATE OR IGNORE `+t+` SET user_id = ? WHERE user_id = ?`, uid, parked)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			im.rep.Reowned += int(n)
			// What is left collided with a row the account already has (a staple or mapping
			// of the same name): theirs wins.
			if _, err := im.tx.ExecContext(im.ctx, `DELETE FROM `+t+` WHERE user_id = ?`, parked); err != nil {
				return err
			}
		}
		if err := ensureUser(im.ctx, im.tx, uid); err != nil {
			return err
		}
		if _, err := im.tx.ExecContext(im.ctx, `DELETE FROM recipes_import_log WHERE legacy_kind = 'parked' AND legacy_id = ?`, l); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) noteParked(legacyUser flexID) error {
	if _, ok := im.logged("parked", string(legacyUser)); ok {
		return nil
	}
	return im.writeLog("parked", string(legacyUser), parkPrefix+string(legacyUser))
}

// --- recipes ---

func (im *importer) recipes() error {
	ings := map[int64][]legacyIngredient{}
	for _, i := range im.b.Ingredients {
		ings[i.RecipeID] = append(ings[i.RecipeID], i)
	}
	steps := map[int64][]legacyStep{}
	for _, s := range im.b.Steps {
		steps[s.RecipeID] = append(steps[s.RecipeID], s)
	}
	tagName := map[int64]string{}
	for _, t := range im.b.Tags {
		tagName[t.ID] = t.Name
	}
	rtags := map[int64][]int64{}
	for _, rt := range im.b.RecipeTags { // bundle order = legacy physical (attach) order
		rtags[rt.RecipeID] = append(rtags[rt.RecipeID], rt.TagID)
	}
	recs := append([]legacyRecipe(nil), im.b.Recipes...)
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	for _, r := range recs {
		key := strconv.FormatInt(r.ID, 10)
		if n, ok := im.logged("recipe", key); ok {
			im.rep.Counts["recipe"].Already++
			if id, err := strconv.ParseInt(n, 10, 64); err == nil {
				im.recipeIDs[r.ID] = id
			}
			continue
		}
		uid, hid, parked, why := im.owner(r.UserID, r.HouseholdID)
		if why != "" {
			im.skip("recipe", key, why)
			continue
		}
		img, err := im.image(key, r.ImageURL)
		if err != nil {
			return err
		}
		st := strings.ToLower(strings.TrimSpace(r.SourceType))
		if st != "manual" && st != "image" && st != "url" {
			im.rep.PhotoNotes = append(im.rep.PhotoNotes, fmt.Sprintf("recipe %s: source_type %q imported as manual", key, r.SourceType))
			st = "manual"
		}
		created := legacyTS(r.CreatedAt, im.now)
		updated := legacyTS(r.UpdatedAt, "")
		if updated == nil {
			updated = created
		}
		newID, err := im.insert("recipe", "recipes_recipes", r.ID, `user_id, household_id, title, description, image_url,
			source_type, source_url, servings, total_time_minutes, created_at, updated_at`,
			uid, hid, r.Title, r.Description, img, st, r.SourceURL, r.Servings, r.TotalTimeMinutes, created, updated)
		if err != nil {
			return err
		}
		im.recipeIDs[r.ID] = newID
		ri := ings[r.ID]
		sort.Slice(ri, func(i, j int) bool { return ri[i].ID < ri[j].ID })
		for _, i := range ri {
			var qv any
			if i.QuantityValue != nil {
				if f, err := i.QuantityValue.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
					qv = math.Round(f*1e4) / 1e4
				}
			}
			if _, err := im.insert("ingredient", "recipes_ingredients", i.ID, `recipe_id, text, quantity_display, quantity_value, unit`,
				newID, i.Text, i.QuantityDisplay, qv, i.Unit); err != nil {
				return err
			}
		}
		rs := steps[r.ID]
		sort.Slice(rs, func(i, j int) bool { return rs[i].StepNumber < rs[j].StepNumber })
		for _, s := range rs {
			if _, err := im.insert("step", "recipes_steps", s.ID, `recipe_id, step_number, text`, newID, s.StepNumber, s.Text); err != nil {
				return err
			}
		}
		for _, tid := range rtags[r.ID] {
			name := quantity.PyStrip(tagName[tid])
			if name == "" {
				continue
			}
			tagID, err := im.tag(tid, name)
			if err != nil {
				return err
			}
			res, err := im.tx.ExecContext(im.ctx, `INSERT OR IGNORE INTO recipes_recipe_tags (recipe_id, tag_id) VALUES (?, ?)`, newID, tagID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				im.rep.Counts["recipe_tag"].Imported++
			}
		}
		if err := im.owned(uid, parked, r.UserID); err != nil {
			return err
		}
		if err := im.writeLog("recipe", key, strconv.FormatInt(newID, 10)); err != nil {
			return err
		}
	}
	return nil
}

// tag is the jarvisd tag named name (tags are global and merge by name, case-insensitively),
// created under its legacy id when that is free.
func (im *importer) tag(legacyID int64, name string) (int64, error) {
	var id int64
	err := im.tx.QueryRowContext(im.ctx, `SELECT id FROM recipes_tags WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return id, err
	}
	if id, err = im.rowID("recipes_tags", legacyID); err != nil {
		return 0, err
	}
	_, err = im.tx.ExecContext(im.ctx, `INSERT INTO recipes_tags (id, name) VALUES (?, ?)`, id, name)
	return id, err
}

// owned records the author: the shadow row for an account, the parked note otherwise.
func (im *importer) owned(uid string, parked bool, legacyUser flexID) error {
	if parked {
		return im.noteParked(legacyUser)
	}
	return ensureUser(im.ctx, im.tx, uid)
}

// image maps a legacy image_url (see the rules at the top), copying the photo on --apply.
func (im *importer) image(recipeKey string, u *string) (any, error) {
	if u == nil || strings.TrimSpace(*u) == "" {
		if u == nil {
			return nil, nil
		}
		return *u, nil
	}
	raw := strings.TrimSpace(*u)
	var name string
	relativised := false
	switch {
	case strings.HasPrefix(raw, mediaPrefix):
		name = mediaName(raw)
		if name == "" {
			return im.dropPhoto(recipeKey, raw, "not an editor photo name")
		}
	default:
		pu, err := url.Parse(raw)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
			return im.dropPhoto(recipeKey, raw, "neither an http(s) URL nor a /media/ path")
		}
		rest, ok := strings.CutPrefix(pu.Path, mediaPrefix)
		if !ok || rest == "" || strings.Contains(rest, "/") {
			return raw, nil // a site's own image (URL imports hotlink it): kept verbatim
		}
		_, inBundle := im.b.Media[rest]
		if !inBundle && !im.legacyHost(pu.Host) {
			return raw, nil
		}
		name = rest
		relativised = true
	}
	if _, ok := mediaTypes[strings.ToLower(pathpkg.Ext(name))]; !ok || blob.ValidateKey(mediaBlobPrefix+name) != nil {
		return im.dropPhoto(recipeKey, raw, "not an image file name")
	}
	data, ok := im.b.Media[name]
	if !ok {
		return im.dropPhoto(recipeKey, raw, "the bundle has no media/"+name)
	}
	if sn := http.DetectContentType(data); !strings.HasPrefix(sn, "image/") && sn != "application/octet-stream" {
		return im.dropPhoto(recipeKey, raw, "media/"+name+" is "+sn+", not an image")
	}
	if !im.photos[name] {
		im.photos[name] = true
		if err := im.copyPhoto(name, data); err != nil {
			return nil, err
		}
	}
	if relativised {
		im.rep.Photos.Relativised++
	}
	return mediaPrefix + name, nil
}

func (im *importer) legacyHost(host string) bool {
	for _, h := range im.opt.LegacyHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

func (im *importer) dropPhoto(recipeKey, raw, why string) (any, error) {
	im.rep.Photos.Dropped++
	im.rep.PhotoNotes = append(im.rep.PhotoNotes, fmt.Sprintf("recipe %s: image_url %q dropped: %s", recipeKey, raw, why))
	return nil, nil
}

func (im *importer) copyPhoto(name string, data []byte) error {
	key := mediaBlobPrefix + name
	if im.blob == nil {
		return errors.New("no blob store")
	}
	if _, err := im.blob.Stat(im.ctx, key); err == nil {
		im.rep.Photos.Present++
		return nil
	} else if !errors.Is(err, blob.ErrNotFound) {
		return err
	}
	im.rep.Photos.Copied++
	if !im.opt.Apply {
		return nil
	}
	_, err := im.blob.Put(im.ctx, key, bytes.NewReader(data), mediaTypes[strings.ToLower(pathpkg.Ext(name))])
	return err
}

// --- meal plans ---

func (im *importer) plans() error {
	ps := append([]legacyMealPlan(nil), im.b.MealPlans...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
	for _, p := range ps {
		key := strconv.FormatInt(p.ID, 10)
		if n, ok := im.logged("meal_plan", key); ok {
			im.rep.Counts["meal_plan"].Already++
			if id, err := strconv.ParseInt(n, 10, 64); err == nil {
				im.planIDs[p.ID] = id
			}
			continue
		}
		uid, hid, parked, why := im.owner(p.UserID, p.HouseholdID)
		if why != "" {
			im.skip("meal_plan", key, why)
			continue
		}
		newID, err := im.insert("meal_plan", "recipes_meal_plans", p.ID, `user_id, household_id, name, start_date, created_at`,
			uid, hid, p.Name, legacyDate(p.StartDate), legacyTS(p.CreatedAt, im.now))
		if err != nil {
			return err
		}
		im.planIDs[p.ID] = newID
		if err := im.owned(uid, parked, p.UserID); err != nil {
			return err
		}
		if err := im.writeLog("meal_plan", key, strconv.FormatInt(newID, 10)); err != nil {
			return err
		}
	}
	return nil
}

func legacyDate(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func (im *importer) exists(table string, id int64) (bool, error) {
	var one int
	err := im.tx.QueryRowContext(im.ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (im *importer) planItems() error {
	items := append([]legacyPlanItem(nil), im.b.PlanItems...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	for _, it := range items {
		if err := im.planItem(it); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) planItem(it legacyPlanItem) error {
	key := strconv.FormatInt(it.ID, 10)
	if _, ok := im.logged("meal_plan_item", key); ok {
		im.rep.Counts["meal_plan_item"].Already++
		return nil
	}
	plan, ok := im.planIDs[it.MealPlanID]
	if !ok {
		im.skip("meal_plan_item", key, fmt.Sprintf("its plan %d is not imported", it.MealPlanID))
		return nil
	}
	rec, ok := im.recipeIDs[it.RecipeID]
	if !ok {
		im.skip("meal_plan_item", key, fmt.Sprintf("its recipe %d is not imported", it.RecipeID))
		return nil
	}
	// An earlier run's plan or recipe may have been deleted in jarvisd since.
	for _, ref := range []struct {
		table string
		id    int64
		what  string
	}{{"recipes_meal_plans", plan, "plan"}, {"recipes_recipes", rec, "recipe"}} {
		ok, err := im.exists(ref.table, ref.id)
		if err != nil {
			return err
		}
		if !ok {
			im.skip("meal_plan_item", key, fmt.Sprintf("its %s (jarvisd id %d) was deleted after an earlier import", ref.what, ref.id))
			return nil
		}
	}
	newID, err := im.insert("meal_plan_item", "recipes_meal_plan_items", it.ID, `meal_plan_id, recipe_id, date, meal_type`,
		plan, rec, legacyDate(it.Date), it.MealType)
	if err != nil {
		return err
	}
	return im.writeLog("meal_plan_item", key, strconv.FormatInt(newID, 10))
}

// --- staples and SKU mappings ---

func (im *importer) staples() error {
	ss := append([]legacyStaple(nil), im.b.Staples...)
	sort.Slice(ss, func(i, j int) bool { return ss[i].ID < ss[j].ID })
	for _, s := range ss {
		key := strconv.FormatInt(s.ID, 10)
		if _, ok := im.logged("staple", key); ok {
			im.rep.Counts["staple"].Already++
			continue
		}
		uid, hid, parked, why := im.owner(s.UserID, s.HouseholdID)
		if why != "" {
			im.skip("staple", key, why)
			continue
		}
		// The same name already visible in that scope, or held by the same author (the table's
		// UNIQUE(user_id, name)): match it rather than duplicate it.
		var existing int64
		err := im.tx.QueryRowContext(im.ctx, `SELECT id FROM recipes_staples
			WHERE name = ? AND (user_id = ? OR (household_id IS NOT NULL AND household_id = ?)) ORDER BY id LIMIT 1`,
			s.Name, uid, hid).Scan(&existing)
		if err == nil {
			im.rep.Counts["staple"].Matched++
			if err := im.writeLog("staple", key, strconv.FormatInt(existing, 10)); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		newID, err := im.insert("staple", "recipes_staples", s.ID, `user_id, household_id, name, created_at`,
			uid, hid, s.Name, legacyTS(s.CreatedAt, im.now))
		if err != nil {
			return err
		}
		if err := im.owned(uid, parked, s.UserID); err != nil {
			return err
		}
		if err := im.writeLog("staple", key, strconv.FormatInt(newID, 10)); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) skuMap() error {
	ms := append([]legacySku(nil), im.b.SkuMap...)
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
	for _, m := range ms {
		key := strconv.FormatInt(m.ID, 10)
		if _, ok := im.logged("sku_map", key); ok {
			im.rep.Counts["sku_map"].Already++
			continue
		}
		uid, hid, parked, why := im.owner(m.UserID, m.HouseholdID)
		if why != "" {
			im.skip("sku_map", key, why)
			continue
		}
		var existing int64
		err := im.tx.QueryRowContext(im.ctx, `SELECT id FROM recipes_grocery_sku_map WHERE COALESCE(household_id, '') = COALESCE(?, '')
			AND user_id = ? AND retailer = ? AND ingredient_name = ?`, hid, uid, m.Retailer, m.IngredientName).Scan(&existing)
		if err == nil {
			im.rep.Counts["sku_map"].Matched++
			if err := im.writeLog("sku_map", key, strconv.FormatInt(existing, 10)); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		source := strings.ToLower(m.Source)
		if source == "" {
			source = "manual"
		}
		created := legacyTS(m.CreatedAt, im.now)
		newID, err := im.insert("sku_map", "recipes_grocery_sku_map", m.ID, `user_id, household_id, retailer, ingredient_name, sku,
			product_name, unit_size, source, created_at, updated_at`,
			uid, hid, m.Retailer, m.IngredientName, m.Sku, m.ProductName, m.UnitSize, source, created, legacyTS(m.UpdatedAt, ""))
		if err != nil {
			return err
		}
		if err := im.owned(uid, parked, m.UserID); err != nil {
			return err
		}
		if err := im.writeLog("sku_map", key, strconv.FormatInt(newID, 10)); err != nil {
			return err
		}
	}
	return nil
}

// --- the report ---

// Write prints the report for a person.
func (r *ImportReport) Write(w io.Writer) {
	mode := "DRY RUN: nothing written (re-run with --apply)"
	if r.Applied {
		mode = "APPLIED"
	}
	if len(r.Refusals) > 0 {
		mode = "REFUSED: nothing written"
	}
	fmt.Fprintf(w, "recipes import (%s)\n", mode)
	if r.Source != "" || r.ExportedAt != "" {
		fmt.Fprintf(w, "bundle: exported %s from %s\n", r.ExportedAt, r.Source)
	}
	fmt.Fprintf(w, "\nUsers matched (%d):\n", len(r.MatchedUsers))
	for _, m := range r.MatchedUsers {
		extra := ""
		if m.Earlier {
			extra = "  (earlier run)"
		}
		fmt.Fprintf(w, "  legacy %s -> jarvisd %d  %s%s\n", m.Legacy, m.New, MaskEmail(m.Email), extra)
	}
	fmt.Fprintf(w, "Users not matched (%d): their rows wait for a later run\n", len(r.UnmatchedUsers))
	for _, m := range r.UnmatchedUsers {
		e := ""
		if m.Email != "" {
			e = "  " + MaskEmail(m.Email)
		}
		fmt.Fprintf(w, "  legacy %s%s: %s\n", m.Legacy, e, m.Reason)
	}
	fmt.Fprintf(w, "Households mapped (%d):\n", len(r.Households))
	for _, h := range r.Households {
		fmt.Fprintf(w, "  %s -> %s (%s) [%s]\n", h.Legacy, h.New, h.Name, h.How)
	}
	fmt.Fprintf(w, "Households not mapped (%d): their rows wait\n", len(r.UnmappedHouseholds))
	for _, h := range r.UnmappedHouseholds {
		fmt.Fprintf(w, "  %s: %s\n", h.Legacy, h.Reason)
	}
	if len(r.Refusals) > 0 {
		fmt.Fprintln(w, "\nREFUSED, nothing was written:")
		for _, e := range r.Refusals {
			fmt.Fprintf(w, "  %s\n", e)
		}
		return
	}
	fmt.Fprintf(w, "\n%-16s %8s %8s %8s %8s %10s\n", "rows", "imported", "matched", "earlier", "skipped", "new id")
	for _, k := range ImportKinds {
		c := r.Counts[k]
		fmt.Fprintf(w, "%-16s %8d %8d %8d %8d %10d\n", k, c.Imported, c.Matched, c.Already, c.Skipped, c.IDChanged)
	}
	if r.Reowned > 0 {
		fmt.Fprintf(w, "parked rows given to their author's new account: %d\n", r.Reowned)
	}
	verb := "to copy"
	if r.Applied {
		verb = "copied"
	}
	fmt.Fprintf(w, "photos: %d %s, %d already in the blob store, %d dropped, %d absolute URLs made relative\n",
		r.Photos.Copied, verb, r.Photos.Present, r.Photos.Dropped, r.Photos.Relativised)
	for _, n := range r.PhotoNotes {
		fmt.Fprintf(w, "  %s\n", n)
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(w, "Skipped rows (%d):\n", len(r.Skipped))
		for _, s := range r.Skipped {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
}

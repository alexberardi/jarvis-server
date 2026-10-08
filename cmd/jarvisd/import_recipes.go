package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/auth"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// import-recipes: the recipes cutover import (docs/recipes/00-inventory.md §13, R11) of a
// bundle written by scripts/legacy/recipes-export.sh. A dry run unless --apply.

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// errImportRefused ends the command non-zero after the report says why.
var errImportRefused = errors.New("import refused (see the report); nothing was written")

func runImportRecipes(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("import-recipes", flag.ContinueOnError)
	fs.SetOutput(stdout)
	apply := fs.Bool("apply", false, "write the import (default: a dry run that reports what it would do)")
	park := fs.Bool("park-unmatched", false, "import household rows of legacy users without an account now, owned by nobody until they sign up")
	var households, hosts multiFlag
	fs.Var(&households, "household", "LEGACY_ID=JARVISD_ID: map a legacy household by hand (repeatable)")
	fs.Var(&hosts, "legacy-host", "HOST[:PORT] of the legacy recipes server: absolute image URLs to its /media become relative (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "usage: jarvisd import-recipes [--apply] [--household OLD=NEW]... [--park-unmatched] [--legacy-host HOST[:PORT]]... BUNDLE")
		fs.PrintDefaults()
	}
	// Flags may come before or after the bundle path.
	var bundle string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		if bundle != "" {
			fs.Usage()
			return errors.New("import-recipes takes one bundle")
		}
		bundle, args = fs.Arg(0), fs.Args()[1:]
	}
	if bundle == "" {
		fs.Usage()
		return errors.New("import-recipes needs the bundle from scripts/legacy/recipes-export.sh")
	}
	overrides := map[string]string{}
	for _, h := range households {
		old, nh, ok := strings.Cut(h, "=")
		if !ok || strings.TrimSpace(old) == "" || strings.TrimSpace(nh) == "" {
			return fmt.Errorf("--household takes LEGACY_ID=JARVISD_ID, got %q", h)
		}
		overrides[strings.TrimSpace(old)] = strings.TrimSpace(nh)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := checkImportUser(cfg.Home); err != nil {
		return err
	}
	b, err := recipes.LoadBundle(bundle)
	if err != nil {
		return err
	}
	deps, err := openDeps(ctx)
	if err != nil {
		return err
	}
	defer deps.DB.Close()
	for _, m := range []struct {
		name string
		st   func() (db.MigrationStatus, error)
	}{
		{"auth", func() (db.MigrationStatus, error) { return db.Status(ctx, deps.DB, "auth", auth.Migrations()) }},
		{"recipes", func() (db.MigrationStatus, error) { return db.Status(ctx, deps.DB, "recipes", recipes.Migrations()) }},
	} {
		st, err := m.st()
		if err != nil {
			return err
		}
		if st.Current == 0 || st.Pending > 0 {
			return fmt.Errorf("the %s module's database is at migration %d of %d: start jarvisd %s once (it migrates on start), then re-run",
				m.name, st.Current, st.Latest, version)
		}
	}
	dir, err := auth.ReadDirectory(ctx, deps.DB.Read)
	if err != nil {
		return err
	}
	acc := recipes.Accounts{Emails: dir.Emails, HouseholdNames: dir.Households}
	for _, m := range dir.Memberships {
		acc.Memberships = append(acc.Memberships, recipes.Membership{HouseholdID: m.HouseholdID, UserID: m.UserID, Role: m.Role})
	}
	rep, err := recipes.Import(ctx, deps.DB, deps.Blobs, b, acc, recipes.ImportOptions{
		Apply: *apply, Households: overrides, ParkUnmatched: *park, LegacyHosts: hosts,
	})
	if err != nil {
		return err
	}
	rep.Write(stdout)
	if len(rep.Refusals) > 0 {
		return errImportRefused
	}
	return nil
}

// checkImportUser refuses to run as root against a home another account owns (the system
// unit's): photos written as root would be unreadable to jarvisd.
func checkImportUser(home string) error {
	fi, err := os.Stat(home)
	if err != nil {
		return err
	}
	owner, ok := homeOwner(fi)
	if !ok || os.Geteuid() != 0 || owner == 0 {
		return nil
	}
	return fmt.Errorf("%s belongs to another account: run this as that account, e.g. sudo -u jarvisd jarvisd import-recipes --home %s …", home, home)
}

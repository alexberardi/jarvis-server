package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alexberardi/jarvis-server/internal/update"
)

// StageOptions say what Stage installs.
type StageOptions struct {
	Target string // the release tag
	TaskID string // jarvisd's update task, when a member asked for it
	By     string // ByAuto, ByServer, ByCLI
	Force  bool   // let a development build update (never allows an older version)
}

// Stage fetches release o.Target's agent archive, verifies it (signature, comment, checksum),
// unpacks the new binary or app bundle into the staged directory, checks it runs as that
// version (and, for a bundle, its code signature), and writes the marker. Nothing in use is
// touched: Finish does the swap.
func (u *Updater) Stage(ctx context.Context, o StageOptions) (*Marker, error) {
	if err := u.Allowed(o.Target, o.Force); err != nil {
		return nil, err
	}
	if m, err := ReadMarker(u.Paths); err != nil {
		return nil, err
	} else if m != nil {
		if u.now().Sub(m.At) < staleAfter {
			return nil, fmt.Errorf("%w (%s → %s, %s)", ErrInProgress, m.From, m.To, m.State)
		}
		u.log().Warn("update: an earlier update never finished; starting afresh", "from", m.From, "to", m.To, "state", m.State)
		_ = WriteResult(u.Paths, &Result{Outcome: OutcomeFailed, From: m.From, To: m.To, TaskID: m.TaskID, By: m.By,
			Reason: "interrupted (the computer slept or restarted during the update)", At: u.now()})
		_ = os.Remove(u.Paths.MarkerPath())
	}
	keys, err := u.keys()
	if err != nil {
		return nil, err
	}
	u.progress("checking", 0)
	rel, err := u.Source.ByTag(ctx, o.Target)
	if err != nil {
		return nil, err
	}
	name := u.AssetName(o.Target)
	dir := u.Paths.Staged()
	if err := os.MkdirAll(u.Paths.Dir, 0o700); err != nil {
		return nil, err
	}
	u.progress("downloading", 0)
	f, err := update.FetchSigned(ctx, u.Source, rel, name, keys, dir, func(n int64) { u.progress("downloading", n) })
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Marker, error) {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	u.progress("testing", 0)
	check := filepath.Join(dir, "check")
	if u.Target.GOOS == "windows" {
		check += ".exe" // Windows runs only what is named as a program
	}
	if err := u.unpackAndCheck(f, check); err != nil {
		return fail(err)
	}
	_ = os.RemoveAll(check) // Swap unpacks again, from the re-verified archive
	m := &Marker{State: StateStaged, From: u.Current, To: o.Target, Exe: u.Target.Exe, Bundle: u.Target.Bundle,
		Asset: name, TaskID: o.TaskID, By: o.By, Force: o.Force, At: u.now().UTC()}
	if err := writeMarker(u.Paths, m); err != nil {
		return fail(err)
	}
	return m, nil
}

// unpackAndCheck unpacks the verified archive's new version to dst (a binary, or for a bundle
// target the app) and checks it: it runs here and reports the release's tag, and a bundle is
// validly signed as this app (Codesign). dst is left for Swap's caller to remove.
func (u *Updater) unpackAndCheck(f *update.Fetched, dst string) error {
	check := u.CheckVersion
	if check == nil {
		check = func(bin, tag string) error {
			return update.CheckProgramVersion(context.Background(), bin, tag, update.AgentProgram)
		}
	}
	_ = os.RemoveAll(dst)
	if u.Target.Bundle == "" {
		if err := update.ExtractProgram(f.Archive, dst, update.AgentProgram); err != nil {
			return err
		}
		return check(dst, f.Tag)
	}
	ok, err := update.ArchiveHasTree(f.Archive, BundleName)
	if err != nil {
		return err
	}
	if !ok {
		// Release builds before item 2.2 ship a bare binary for macOS: replacing the one inside
		// the app would break the bundle's signature (and, with it, every permission grant).
		return fmt.Errorf("release %s has no %s for macOS; download the app from the release page instead", f.Tag, BundleName)
	}
	if err := update.ExtractTree(f.Archive, dst, BundleName); err != nil {
		return err
	}
	if err := u.checkBundle(dst); err != nil {
		return err
	}
	return check(filepath.Join(dst, "Contents", "MacOS", update.AgentProgram), f.Tag)
}

// checkBundle requires the new app's code signature to be valid and to be this app's: the
// identifier is the agent's bundle id, and when the installed app is signed by a team, the new
// one is signed by the same team. macOS keys permission grants (Accessibility, Microphone,
// Screen Recording, Automation) on that identity, so a Developer ID update keeps them; an
// ad-hoc signed app has none to keep (its grants key on the exact build).
func (u *Updater) checkBundle(app string) error {
	cs := u.Codesign
	if cs == nil {
		cs = NativeCodesign{}
	}
	if err := cs.Verify(app); err != nil {
		return fmt.Errorf("the new app's code signature isn't valid: %w", err)
	}
	id, team, err := cs.Info(app)
	if err != nil {
		return err
	}
	if id != appIdentifier {
		return fmt.Errorf("the new app is signed as %q, not %s", id, appIdentifier)
	}
	_, curTeam, err := cs.Info(u.Target.Bundle)
	if err != nil {
		// The installed app may be unsigned (a hand-made dev bundle): nothing to keep.
		u.log().Warn("update: the installed app's signature couldn't be read", "err", err)
		return nil
	}
	switch {
	case curTeam != "" && team != curTeam:
		return fmt.Errorf("the new app is signed by team %q, the installed one by %q: refusing (macOS would drop its permissions)", team, curTeam)
	case team == "":
		u.log().Info("update: the new app is ad-hoc signed; macOS will ask for its permissions again")
	}
	return nil
}

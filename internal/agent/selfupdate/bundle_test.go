package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCodesign reads an app's "signature" from its Info.plist ("plist <tag>"): signs maps the
// tag to identifier and team; the installed app is "plist installed".
type fakeCodesign struct{ signs map[string][2]string }

func (c fakeCodesign) tag(app string) string {
	b, _ := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	return strings.TrimPrefix(string(b), "plist ")
}

func (c fakeCodesign) Verify(app string) error {
	if _, ok := c.signs[c.tag(app)]; !ok {
		return errors.New("code object is not signed at all")
	}
	return nil
}

func (c fakeCodesign) Info(app string) (string, string, error) {
	s, ok := c.signs[c.tag(app)]
	if !ok {
		return "", "", errors.New("not signed")
	}
	return s[0], s[1], nil
}

// newBundleFixture installs "Jarvis Automation Agent.app" at v1.0.0, signed by team T1.
func newBundleFixture(t *testing.T) (*fixture, string) {
	f := newFixture(t, "v1.0.0", "darwin")
	bundle := filepath.Join(filepath.Dir(filepath.Dir(f.exe)), "Applications", BundleName)
	exe := filepath.Join(bundle, "Contents", "MacOS", "jarvis-agent")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, fakeBinary("v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte("plist installed"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.exe = exe
	f.u.Target = Target{GOOS: "darwin", Exe: exe, Bundle: bundle}
	f.u.Codesign = fakeCodesign{signs: map[string][2]string{
		"installed": {appIdentifier, "T1"},
		"v1.1.0":    {appIdentifier, "T1"},
		"v1.2.0":    {appIdentifier, "OTHER"},
		"v1.3.0":    {"com.example.evil", "T1"},
		"v1.5.0":    {appIdentifier, "T1"},
	}}
	return f, bundle
}

func TestBundleUpdate(t *testing.T) {
	f, bundle := newBundleFixture(t)
	f.publish("v1.1.0", relOpt{bundle: true})
	f.u.Source = f.rel.source("v1.1.0")
	if _, err := f.u.Stage(context.Background(), StageOptions{Target: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	a, log := f.agent()
	if r := f.u.Finish(context.Background(), *a); r.Outcome != OutcomeSucceeded {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist")); string(b) != "plist v1.1.0" || f.installed() != "v1.1.0" {
		t.Fatalf("bundle not replaced: %s %s", b, f.installed())
	}
	if len(*log) != 1 {
		t.Fatalf("restarts %v", *log)
	}
	entries, _ := os.ReadDir(filepath.Dir(bundle))
	if len(entries) != 1 {
		t.Fatalf("left beside the app: %v", entries)
	}
}

func TestBundleRollback(t *testing.T) {
	f, bundle := newBundleFixture(t)
	f.publish("v1.1.0", relOpt{bundle: true})
	f.u.Source = f.rel.source("v1.1.0")
	if _, err := f.u.Stage(context.Background(), StageOptions{Target: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	a, _ := f.agent("v1.1.0")
	if r := f.u.Finish(context.Background(), *a); r.Outcome != OutcomeRolledBack {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist")); string(b) != "plist installed" || f.installed() != "v1.0.0" {
		t.Fatalf("not restored: %s %s", b, f.installed())
	}
	entries, _ := os.ReadDir(filepath.Dir(bundle))
	if len(entries) != 1 {
		t.Fatalf("left beside the app: %v", entries)
	}
}

func TestBundleRefusals(t *testing.T) {
	f, _ := newBundleFixture(t)
	f.publish("v1.2.0", relOpt{bundle: true})
	f.publish("v1.3.0", relOpt{bundle: true})
	f.publish("v1.4.0", relOpt{bundle: true}) // unsigned app
	f.publish("v1.5.0", relOpt{})             // a bare binary only (releases before item 2.2)
	for tag, want := range map[string]string{
		"v1.2.0": `signed by team "OTHER"`,
		"v1.3.0": `signed as "com.example.evil"`,
		"v1.4.0": "code signature isn't valid",
		"v1.5.0": "has no " + BundleName,
	} {
		f.u.Source = f.rel.source(tag)
		if _, err := f.u.Stage(context.Background(), StageOptions{Target: tag}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", tag, err, want)
		}
		if f.installed() != "v1.0.0" {
			t.Fatalf("%s changed the app", tag)
		}
	}
}

func TestParseCodesignInfo(t *testing.T) {
	out := "Executable=/Applications/X.app/Contents/MacOS/jarvis-agent\nIdentifier=net.jarvisautomation.agent\nFormat=app bundle\nTeamIdentifier=ABCDE12345\n"
	if id, team, err := parseCodesignInfo(out); err != nil || id != appIdentifier || team != "ABCDE12345" {
		t.Fatal(id, team, err)
	}
	if _, team, _ := parseCodesignInfo("Identifier=x\nTeamIdentifier=not set\n"); team != "" {
		t.Fatalf("ad hoc team %q", team)
	}
	if _, _, err := parseCodesignInfo("nothing"); err == nil {
		t.Fatal("no identifier accepted")
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func l1Baseline(t *testing.T) (conf map[string]string, shown string) {
	t.Helper()
	c, err := parsePJSIPConf(filepath.Join("..", "..", "test", "sip", "asterisk", "pjsip.conf"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "l1-show-100.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return jarvisExtension(c), string(b)
}

// The captured L1 output is what pjsip.conf's extension 100 looks like once Asterisk has read
// it: every setting the conf spells out shows up, in Asterisk's spelling. A changed pjsip.conf
// fails here until the fixture is captured again.
func TestL1BaselineMatchesConf(t *testing.T) {
	conf, shown := l1Baseline(t)
	if got := drift(conf, shownSettings(shown), shownSettings(shown)); len(got) != 0 {
		t.Fatalf("the L1 capture disagrees with pjsip.conf:\n%s", strings.Join(got, "\n"))
	}
	if conf["rtp_timeout"] != "30" || conf["max_contacts"] != "1" {
		t.Fatalf("extension 100's endpoint and aor not both read: %v", conf)
	}
}

func TestDriftReportsAChangedDefault(t *testing.T) {
	conf, shown := l1Baseline(t)
	freepbx := strings.Replace(shown, " rtp_timeout                        : 30\n", " rtp_timeout                        : 60\n", 1)
	got := drift(conf, shownSettings(shown), shownSettings(freepbx))
	if len(got) != 1 || !strings.Contains(got[0], "rtp_timeout") || !strings.Contains(got[0], `"60"`) {
		t.Fatalf("got %q, want rtp_timeout 30 → 60", got)
	}
}

func TestDriftReportsAMissingSetting(t *testing.T) {
	conf, shown := l1Baseline(t)
	freepbx := shownSettings(shown)
	delete(freepbx, "timers")
	got := drift(conf, shownSettings(shown), freepbx)
	if len(got) != 1 || !strings.Contains(got[0], "timers") {
		t.Fatalf("got %q, want timers missing", got)
	}
}

// Settings pjsip.conf leaves to Asterisk count too: FreePBX may set one L1 does not copy.
func TestDriftReportsASettingL1LeavesToAsterisk(t *testing.T) {
	conf, shown := l1Baseline(t)
	freepbx := shownSettings(shown)
	freepbx["moh_suggest"] = "jazz"
	freepbx["new_in_asterisk_23"] = "true"
	got := drift(conf, shownSettings(shown), freepbx)
	if len(got) != 2 || !strings.Contains(got[0], "moh_suggest") || !strings.Contains(got[1], "new_in_asterisk_23") {
		t.Fatalf("got %q, want moh_suggest and new_in_asterisk_23", got)
	}
}

// What names this install (its contact, caller ID, auth section, voicemail) is not drift.
func TestDriftIgnoresTheInstallsOwnNames(t *testing.T) {
	conf, shown := l1Baseline(t)
	freepbx := shownSettings(shown)
	freepbx["contact"] = "sip:100@10.0.2.2:5060"
	freepbx["callerid"] = `"Jarvis FreePBX" <100>`
	freepbx["mailboxes"] = "100@device"
	freepbx["transport"] = "0.0.0.0-udp"
	if got := drift(conf, shownSettings(shown), freepbx); len(got) != 0 {
		t.Fatalf("got %q, want no drift", got)
	}
}

func TestSameSetting(t *testing.T) {
	for _, c := range [][2]string{
		{"yes", "true"}, {"no", "false"}, {"No", "no"}, {"3.0", "3.000000"}, {"ef", "184"},
		{"ulaw,alaw,gsm", "(ulaw|alaw|gsm)"}, {"", ""}, {"on", "yes"}, {"off", "no"},
	} {
		if !sameSetting(c[0], c[1]) {
			t.Errorf("%q and %q should be the same setting", c[0], c[1])
		}
	}
	for _, c := range [][2]string{{"yes", "false"}, {"on", "off"}, {"30", "60"}, {"ulaw,alaw", "(alaw|ulaw)"}, {"ef", "0"}} {
		if sameSetting(c[0], c[1]) {
			t.Errorf("%q and %q should differ", c[0], c[1])
		}
	}
}

func TestDriftCommand(t *testing.T) {
	dir := t.TempDir()
	b, _ := os.ReadFile(filepath.Join("testdata", "l1-show-100.txt"))
	same := filepath.Join(dir, "same.txt")
	os.WriteFile(same, b, 0o644)
	var out strings.Builder
	if err := run(t.Context(), []string{"drift", same}, &out); err != nil {
		t.Fatalf("no drift: %v\n%s", err, out.String())
	}
	changed := filepath.Join(dir, "changed.txt")
	os.WriteFile(changed, []byte(strings.Replace(string(b), " direct_media                       : true", " direct_media                       : false", 1)), 0o644)
	out.Reset()
	err := run(t.Context(), []string{"drift", changed}, &out)
	if _, ok := err.(errDrift); !ok || !strings.Contains(out.String(), "direct_media") {
		t.Fatalf("got %v\n%s", err, out.String())
	}
}

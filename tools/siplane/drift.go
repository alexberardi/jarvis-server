package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The L2 lane's drift check (docs/cc/phone-sip.md §5): a real FreePBX 17's
// `pjsip show endpoint 100` + `pjsip show aor 100` against what L1 copies.
//
//	go run ./tools/siplane drift [--conf pjsip.conf] [--baseline FILE] FREEPBX.txt
//
// Exits 1 on drift, 2 when a file is missing. scripts/freepbx-lane weekly runs it.

const (
	driftConf     = "test/sip/asterisk/pjsip.conf"
	driftBaseline = "tools/siplane/testdata/l1-show-100.txt"
)

// driftIgnored names this install rather than FreePBX's defaults: where the extension
// registered from, its caller ID, its auth section, voicemail and transport names.
var driftIgnored = map[string]bool{
	"type": true, "aors": true, "auth": true, "outbound_auth": true, "callerid": true,
	"contact": true, "mailboxes": true, "voicemail_extension": true, "transport": true,
}

type errDrift struct{ n int }

func (e errDrift) Error() string { return fmt.Sprintf("%d settings drifted from L1", e.n) }

// jarvisExtension is pjsip.conf's extension 100, endpoint and aor in one map, as
// `pjsip show endpoint` and `pjsip show aor` are read together.
func jarvisExtension(conf map[string]map[string]string) map[string]string {
	m := map[string]string{}
	for _, sec := range []string{"100/endpoint", "100/aor"} {
		for k, v := range conf[sec] {
			m[k] = v
		}
	}
	return m
}

// drift lists how FreePBX's extension differs from L1's, sorted by setting: first what
// pjsip.conf spells out (conf, in its own spelling), then everything else Asterisk shows,
// against L1's running Asterisk (baseline).
func drift(conf, baseline, freepbx map[string]string) []string {
	var out []string
	for k, want := range conf {
		if driftIgnored[k] {
			continue
		}
		got, ok := freepbx[k]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s: L1 copies %q, FreePBX does not show it", k, want))
		case !sameSetting(want, got):
			out = append(out, fmt.Sprintf("%s: L1 copies %q, FreePBX has %q", k, want, got))
		}
	}
	keys := map[string]bool{}
	for k := range baseline {
		keys[k] = true
	}
	for k := range freepbx {
		keys[k] = true
	}
	for k := range keys {
		if _, copied := conf[k]; copied || driftIgnored[k] {
			continue
		}
		want, inL1 := baseline[k]
		got, inFreePBX := freepbx[k]
		switch {
		case !inFreePBX:
			out = append(out, fmt.Sprintf("%s: L1's Asterisk shows %q, FreePBX does not show it", k, want))
		case !inL1:
			out = append(out, fmt.Sprintf("%s: FreePBX has %q, L1's Asterisk does not show it", k, got))
		case !sameSetting(want, got):
			out = append(out, fmt.Sprintf("%s: FreePBX has %q, L1's Asterisk %q (pjsip.conf leaves it to Asterisk)", k, got, want))
		}
	}
	sort.Strings(out)
	return out
}

// tosValues are the DSCP names pjsip.conf may use, as Asterisk shows them (DSCP << 2).
var tosValues = map[string]string{
	"cs0": "0", "cs1": "32", "cs2": "64", "cs3": "96", "cs4": "128", "cs5": "160", "cs6": "192", "cs7": "224",
	"af11": "40", "af12": "48", "af13": "56", "af21": "72", "af22": "80", "af23": "88",
	"af31": "104", "af32": "112", "af33": "120", "af41": "136", "af42": "144", "af43": "152", "ef": "184",
}

// sameSetting compares a setting as written (pjsip.conf) or shown in either spelling Asterisk
// accepts: yes/true/on, no/false/off, 3.0 and 3.000000, ef and 184, a,b and (a|b).
func sameSetting(a, b string) bool {
	return settingValue(a) == settingValue(b)
}

func settingValue(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "yes", "true", "on":
		return "true"
	case "no", "false", "off":
		return "false"
	}
	if v, ok := tosValues[s]; ok {
		return v
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		s = strings.ReplaceAll(s[1:len(s)-1], "|", ",")
	}
	return s
}

func runDrift(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("siplane drift", flag.ContinueOnError)
	confPath := fs.String("conf", "", "L1's pjsip.conf (default "+driftConf+")")
	basePath := fs.String("baseline", "", "L1's captured `pjsip show endpoint/aor 100` (default "+driftBaseline+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: siplane drift [--conf F] [--baseline F] FREEPBX.txt (pjsip show endpoint 100, then aor 100)")
	}
	if *confPath == "" || *basePath == "" {
		root, err := repoRoot()
		if err != nil {
			return err
		}
		if *confPath == "" {
			*confPath = filepath.Join(root, driftConf)
		}
		if *basePath == "" {
			*basePath = filepath.Join(root, driftBaseline)
		}
	}
	conf, err := parsePJSIPConf(*confPath)
	if err != nil {
		return err
	}
	base, err := os.ReadFile(*basePath)
	if err != nil {
		return err
	}
	got, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	freepbx := shownSettings(string(got))
	if len(freepbx) == 0 {
		return fmt.Errorf("%s has no settings (not `pjsip show endpoint 100` output?)", fs.Arg(0))
	}
	d := drift(jarvisExtension(conf), shownSettings(string(base)), freepbx)
	if len(d) == 0 {
		fmt.Fprintf(stdout, "no drift: FreePBX's extension 100 matches L1 (%d settings)\n", len(freepbx))
		return nil
	}
	fmt.Fprintf(stdout, "drift: FreePBX's extension 100 differs from L1 in %d settings:\n", len(d))
	for _, line := range d {
		fmt.Fprintln(stdout, "  "+line)
	}
	return errDrift{len(d)}
}

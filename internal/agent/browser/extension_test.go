package browser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func manifestOf(t *testing.T, target string) map[string]any {
	t.Helper()
	files, err := ExtensionFiles(target)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func backgroundJS(t *testing.T) string {
	t.Helper()
	b, err := extensionFS.ReadFile("extension/background.js")
	if err != nil {
		t.Fatal(err)
	}
	// Windows checkouts convert the script to CRLF; the source guards match LF.
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// The unpacked id Chromium assigns comes from the manifest's public key: sha256 of the DER,
// first 32 hex digits mapped 0-f -> a-p. AllowedOrigins must name exactly that id.
func TestChromeExtensionIDMatchesKey(t *testing.T) {
	m := manifestOf(t, TargetChromium)
	der, err := base64.StdEncoding.DecodeString(m["key"].(string))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	var id strings.Builder
	for _, c := range hex.EncodeToString(sum[:])[:32] {
		v, _ := strconv.ParseUint(string(c), 16, 8)
		id.WriteByte(byte('a' + v))
	}
	if id.String() != ChromeDevExtensionID {
		t.Fatalf("key gives id %s, code says %s", id.String(), ChromeDevExtensionID)
	}
}

func TestManifestPerTarget(t *testing.T) {
	ff := manifestOf(t, TargetFirefox)
	if _, has := ff["key"]; has {
		t.Error("firefox manifest keeps the Chrome key")
	}
	if bg := ff["background"].(map[string]any); bg["service_worker"] != nil || bg["scripts"] == nil {
		t.Errorf("firefox background %v", bg)
	}
	gecko := ff["browser_specific_settings"].(map[string]any)["gecko"].(map[string]any)
	if gecko["id"] != FirefoxExtensionID {
		t.Errorf("gecko id %v", gecko["id"])
	}
	cr := manifestOf(t, TargetChromium)
	if _, has := cr["browser_specific_settings"]; has {
		t.Error("chromium manifest keeps Firefox settings")
	}
	if bg := cr["background"].(map[string]any); bg["scripts"] != nil || bg["service_worker"] != "background.js" {
		t.Errorf("chromium background %v", bg)
	}
	if _, err := ExtensionFiles("safari"); err == nil {
		t.Error("unknown target accepted")
	}
	if ExtensionVersion() == "" || ff["version"] != ExtensionVersion() {
		t.Errorf("version %q vs %v", ExtensionVersion(), ff["version"])
	}
}

// The stores' limits on what the manifest shows: the Chrome Web Store takes the listing's
// summary from "description" and refuses more than 132 characters; AMO caps names at 50.
func TestManifestStoreLimits(t *testing.T) {
	m := manifestOf(t, TargetChromium)
	if d, _ := m["description"].(string); d == "" || utf8.RuneCountInString(d) > 132 {
		t.Errorf("description is %d characters (Chrome Web Store: 1-132): %q", utf8.RuneCountInString(d), d)
	}
	if n, _ := m["name"].(string); n == "" || utf8.RuneCountInString(n) > 50 {
		t.Errorf("name %q (AMO: at most 50 characters)", n)
	}
}

// What the extension may do is a closed set: nothing lets a web page or another extension
// talk to it, and no permission beyond the basic set.
func TestExtensionSurface(t *testing.T) {
	for _, target := range []string{TargetFirefox, TargetChromium} {
		m := manifestOf(t, target)
		var perms []string
		for _, p := range m["permissions"].([]any) {
			perms = append(perms, p.(string))
		}
		sort.Strings(perms)
		if got := strings.Join(perms, ","); got != "alarms,downloads,nativeMessaging,scripting,tabs" {
			t.Errorf("%s permissions %s", target, got)
		}
		for _, k := range []string{"externally_connectable", "content_scripts", "web_accessible_resources", "optional_permissions", "content_security_policy"} {
			if _, has := m[k]; has {
				t.Errorf("%s manifest has %s", target, k)
			}
		}
	}
	js := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(backgroundJS(t), "") // code, not comments
	// snapCollect (computer control's snapshot) is the one place that reads field values, and
	// never a password, hidden, card or one-time-code field's.
	snap := jsFunc(t, js, "snapCollect")
	for _, guard := range []string{`t === "password"`, `t === "hidden"`, `a.startsWith("cc-")`, `a === "one-time-code"`, "if (secret(el)) return undefined;"} {
		if !strings.Contains(snap, guard) {
			t.Errorf("snapCollect lost its secret-field guard %q", guard)
		}
	}
	// recInstall (recording) is the only other: its events carry no value for those fields, nor
	// for password-manager fields or fields labelled as a card, CVV, SSN or PIN (masked instead).
	rec := jsFunc(t, js, "recInstall")
	for _, guard := range []string{`t === "password"`, `t === "hidden"`, `a.startsWith("cc-")`, `a === "one-time-code"`,
		`a === "current-password"`, `a === "new-password"`, "REC_SECRET_LABEL.test(", "if (secret(el)) return undefined;",
		"e.masked = true;", "if (!ev.isTrusted) return;"} {
		if !strings.Contains(rec, guard) {
			t.Errorf("recInstall lost its secret-field guard %q", guard)
		}
	}
	for _, term := range []string{"card", "cvv", "ssn", "pin"} {
		if m := regexp.MustCompile(`const REC_SECRET_LABEL = (/.*/i);`).FindStringSubmatch(rec); m == nil || !strings.Contains(m[1], term) {
			t.Errorf("recInstall's REC_SECRET_LABEL doesn't match %q", term)
		}
	}
	// act never types into a password field.
	for _, guard := range []string{`type === "password"`, `code: "password_field"`} {
		if !strings.Contains(jsFunc(t, js, "actDo"), guard) {
			t.Errorf("actDo lost its password-field refusal %q", guard)
		}
	}
	// act finds its element only through the extension's own isolated-world state: the page can
	// write data-jarvis-* attributes itself, so they never pick the element.
	for _, fn := range []string{"actFind", "actDo"} {
		for _, banned := range []string{"querySelector", "getAttribute(\"data-jarvis"} {
			if strings.Contains(jsFunc(t, js, fn), banned) {
				t.Errorf("%s looks the element up in the page's DOM (%q)", fn, banned)
			}
		}
	}
	for _, banned := range []string{"onMessageExternal", "onConnectExternal", "runtime.onMessage", "runtime.onConnect.",
		"eval(", "new Function", "innerHTML", ".value", "fetch(", "XMLHttpRequest", "WebSocket"} {
		in := js
		if banned == ".value" { // snapCollect and recInstall alone read values; every other ban holds there too
			in = strings.Replace(strings.Replace(js, snap, "", 1), rec, "", 1)
		}
		if strings.Contains(in, banned) {
			t.Errorf("background.js contains %q", banned)
		}
	}
	if !regexp.MustCompile(`const HOST = "` + regexp.QuoteMeta(HostName) + `"`).MatchString(js) {
		t.Error("background.js connects to another host name")
	}
	if !regexp.MustCompile(`const PROTOCOL = ` + strconv.Itoa(Protocol) + `;`).MatchString(js) {
		t.Error("background.js speaks another protocol version")
	}
	// Form fields and editable regions are never read.
	for _, tag := range []string{`"input"`, `"textarea"`, `"select"`, `"option"`} {
		if !strings.Contains(js, tag) {
			t.Errorf("extractText doesn't skip %s", tag)
		}
	}
	if !strings.Contains(js, "isContentEditable") {
		t.Error("extractText doesn't skip contenteditable")
	}
}

// jsFunc returns the source of the top-level function name in js.
func jsFunc(t *testing.T, js, name string) string {
	t.Helper()
	i := strings.Index(js, "\nfunction "+name+"(")
	if i < 0 {
		t.Fatalf("background.js has no function %s", name)
	}
	f := js[i+1:]
	return f[:strings.Index(f, "\n}\n")+2]
}

// Every command a tool sends exists in the extension, and the extension has none no tool uses.
func TestCommandParity(t *testing.T) {
	js := backgroundJS(t)
	block := js[strings.Index(js, "const COMMANDS = {"):]
	block = block[:strings.Index(block, "\n};")]
	var inJS []string
	for _, m := range regexp.MustCompile(`(?m)^  async (\w+)\(args\)`).FindAllStringSubmatch(block, -1) {
		inJS = append(inJS, m[1])
	}
	sort.Strings(inJS)

	h, path := startHub(t)
	jpeg1x1 := jpegB64(t, 1, 1)
	var mu sync.Mutex
	sent := map[string]bool{}
	connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		mu.Lock()
		sent[m.Cmd] = true
		mu.Unlock()
		return ok(map[string]any{"tabs": []any{}, "text": "x", "context": "", "gen": 1, "elements": []any{}, "ok": true,
			"tab": map[string]any{"tab_id": 1}, "window_id": 2,
			"data": jpeg1x1, "width": 1, "height": 1, "device_pixel_ratio": 1, "path": "/d/x.csv", "bytes": 1, "filename": "x.csv"})
	})
	ts := NewTools(h)
	args := map[string]map[string]any{
		ToolOpen: {"url": "https://example.com"}, ToolSwitchTab: {"tab_id": "firefox:1"}, ToolCloseTab: {"tab_id": "1"},
		ToolNavigate: {"action": "back"}, ToolMedia: {"action": "pause"}, ToolScroll: {"direction": "down"}, ToolFind: {"text": "x"},
		ToolFill: {"field": "username", "text": "abcde"}, ToolClick: {"text": "OK"},
	}
	for _, d := range ts.Available(context.Background()) {
		a := args[d.Name]
		if a == nil {
			a = map[string]any{}
		}
		if out, _ := ts.Call(context.Background(), d.Name, a, node.Request{}); out["success"] != true {
			t.Errorf("%s: %v", d.Name, out)
		}
	}
	// The computer-control commands have Go callers that are not model tools.
	ctx := context.Background()
	if _, err := ts.Snapshot(ctx, "firefox:1", 0); err != nil {
		t.Errorf("Snapshot: %v", err)
	}
	if _, err := ts.Act(ctx, "firefox:1", 1, "e1", "press", "", nil); err != nil {
		t.Errorf("Act: %v", err)
	}
	if _, _, err := ts.OpenBackground(ctx, "https://example.com"); err != nil {
		t.Errorf("OpenBackground: %v", err)
	}
	if err := ts.RecordStart(ctx, "rec_1"); err != nil {
		t.Errorf("RecordStart: %v", err)
	}
	if _, err := ts.RecordStop(ctx, "rec_1"); err != nil {
		t.Errorf("RecordStop: %v", err)
	}
	if _, _, err := ts.Screenshot(ctx, "firefox:2"); err != nil {
		t.Errorf("Screenshot: %v", err)
	}
	if _, err := ts.Download(ctx, "firefox:1", "e1", ""); err != nil {
		t.Errorf("Download: %v", err)
	}
	mu.Lock()
	var fromGo []string
	for c := range sent {
		fromGo = append(fromGo, c)
	}
	mu.Unlock()
	sort.Strings(fromGo)
	if strings.Join(inJS, ",") != strings.Join(fromGo, ",") {
		t.Fatalf("extension commands %v, tools send %v", inJS, fromGo)
	}
}

func TestNoSignedXPIUnlessEmbedded(t *testing.T) {
	b, ok := SignedXPI()
	if ok && (len(b) < 4 || string(b[:2]) != "PK") {
		t.Fatal("signed/ holds something that isn't a zip")
	}
}

// The toolbar popup (install plan 3.5) adds no input to the extension: it is a static page that
// shows the state background.js put in its URL and links to the public download page, and shows
// and asks for website access (permissions.contains/request of <all_urls>, which the manifest
// already declares; jstest/popup.test.mjs). It sends the background nothing (so background.js still needs no runtime.onMessage), runs no remote
// code and reads no page.
func TestPopupSurface(t *testing.T) {
	for _, target := range []string{TargetFirefox, TargetChromium} {
		m := manifestOf(t, target)
		action, _ := m["action"].(map[string]any)
		if action["default_popup"] != "popup.html" {
			t.Errorf("%s action.default_popup = %v", target, action["default_popup"])
		}
		files, err := ExtensionFiles(target)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"popup.html", "popup.js"} {
			if len(files[f]) == 0 {
				t.Errorf("%s has no %s", target, f)
			}
		}
	}
	b, err := extensionFS.ReadFile("extension/popup.js")
	if err != nil {
		t.Fatal(err)
	}
	js := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(b), "")
	for _, banned := range []string{"sendMessage", "runtime.connect", "connectNative", "onMessage", "eval(", "new Function",
		"innerHTML", "fetch(", "XMLHttpRequest", "WebSocket", "scripting.", "storage."} {
		if strings.Contains(js, banned) {
			t.Errorf("popup.js contains %q", banned)
		}
	}
	html, err := extensionFS.ReadFile("extension/popup.html")
	if err != nil {
		t.Fatal(err)
	}
	// One local script and no inline handlers: MV3's default CSP, which the manifest keeps.
	if s := regexp.MustCompile(`<script[^>]*>`).FindAllString(string(html), -1); len(s) != 1 || s[0] != `<script src="popup.js">` {
		t.Errorf("popup.html scripts: %q", s)
	}
	if regexp.MustCompile(`(?i)\son[a-z]+=`).Match(html) {
		t.Error("popup.html has an inline event handler")
	}
	const download = "https://github.com/alexberardi/jarvis-server/releases"
	if !strings.Contains(string(b), `"`+download+`"`) || !strings.Contains(string(html), `"`+download+`"`) {
		t.Errorf("popup doesn't link to %s", download)
	}
	// The background sets the popup's state; it must stay one of the states popup.js knows.
	for _, st := range regexp.MustCompile(`setState\("(\w+)"\)`).FindAllStringSubmatch(backgroundJS(t), -1) {
		if !strings.Contains(js, st[1]+":") {
			t.Errorf("popup.js has no text for state %q", st[1])
		}
	}
}

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
	return string(b)
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
		if got := strings.Join(perms, ","); got != "alarms,nativeMessaging,scripting,tabs" {
			t.Errorf("%s permissions %s", target, got)
		}
		for _, k := range []string{"externally_connectable", "content_scripts", "web_accessible_resources", "optional_permissions", "content_security_policy"} {
			if _, has := m[k]; has {
				t.Errorf("%s manifest has %s", target, k)
			}
		}
	}
	js := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(backgroundJS(t), "") // code, not comments
	for _, banned := range []string{"onMessageExternal", "onConnectExternal", "runtime.onMessage", "runtime.onConnect.",
		"eval(", "new Function", "innerHTML", ".value", "fetch(", "XMLHttpRequest", "WebSocket"} {
		if strings.Contains(js, banned) {
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
	var mu sync.Mutex
	sent := map[string]bool{}
	connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		mu.Lock()
		sent[m.Cmd] = true
		mu.Unlock()
		return ok(map[string]any{"tabs": []any{}, "text": "x", "context": ""})
	})
	ts := NewTools(h)
	args := map[string]map[string]any{
		ToolOpen: {"url": "https://example.com"}, ToolSwitchTab: {"tab_id": "firefox:1"}, ToolCloseTab: {"tab_id": "1"},
		ToolNavigate: {"action": "back"}, ToolMedia: {"action": "pause"}, ToolScroll: {"direction": "down"}, ToolFind: {"text": "x"},
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

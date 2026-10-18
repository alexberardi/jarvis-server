package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const shadeXML = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">
<node index="0" text="" package="com.android.systemui" bounds="[0,0][1080,2400]">
  <node index="1" text="Jarvis" package="com.android.systemui" bounds="[0,100][1080,180]" />
  <node index="2" text="%s" package="com.android.systemui" bounds="[100,300][900,360]" />
</node></hierarchy>`

const openedXML = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">
<node index="0" text="" package="com.jarvis.nodemobile" bounds="[0,0][1080,2400]">
  <node index="1" text="%s" package="com.jarvis.nodemobile" bounds="[40,200][1040,280]" />
</node></hierarchy>`

func TestFindText(t *testing.T) {
	xml := []byte(strings.Replace(shadeXML, "%s", "Front door left open", 1))
	n, ok := findText(xml, "Front door left open")
	if !ok {
		t.Fatal("not found")
	}
	if x, y, err := center(n.Bounds); err != nil || x != 500 || y != 330 {
		t.Errorf("center(%q) = %d, %d, %v; want 500, 330", n.Bounds, x, y, err)
	}
	if n.Package != "com.android.systemui" {
		t.Errorf("package = %q", n.Package)
	}
	if _, ok := findText(xml, "Back door"); ok {
		t.Error("found a text that is not there")
	}
	if _, _, err := center("[1,2]"); err == nil {
		t.Error("center accepted bad bounds")
	}
}

// pushFakes is an adb whose uiautomator dump is the shade with the push until something taps,
// then the app with the item open; a maestro that passes; and the sandbox's two APIs.
type pushFakes struct {
	adb, maestro, calls, dir string
	srv                      *httptest.Server
	mu                       sync.Mutex
	posted                   map[string]any
	tokens                   []string
}

func newPushFakes(t *testing.T, opened bool) *pushFakes {
	f := &pushFakes{dir: t.TempDir(), tokens: []string{"ExponentPushToken[real]"}}
	f.calls = filepath.Join(f.dir, "calls.log")
	f.adb = filepath.Join(f.dir, "adb")
	f.maestro = filepath.Join(f.dir, "maestro")
	write(t, f.maestro, "#!/bin/sh\necho \"maestro $*\" >>"+f.calls+"\n")
	appXML := filepath.Join(f.dir, "opened.xml")
	if !opened {
		appXML = filepath.Join(f.dir, "shade.xml") // the tap did nothing
	}
	write(t, f.adb, `#!/bin/sh
echo "adb $*" >>`+f.calls+`
case "$*" in
  *"input tap"*) touch `+f.dir+`/tapped ;;
  *"screencap -p"*) printf '\211PNG\r\n\032\nfake' ;;
  *"cat /data/local/tmp/e2emobile.xml"*)
    if [ -f `+f.dir+`/tapped ]; then cat `+appXML+`; elif [ -f `+f.dir+`/title ]; then cat `+f.dir+`/shade.xml; else echo '<hierarchy/>'; fi ;;
esac
`)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/tokens/me":
			if r.Header.Get("Authorization") != "Bearer user-jwt" {
				w.WriteHeader(401)
				return
			}
			f.mu.Lock()
			out := []map[string]any{}
			for _, tok := range f.tokens {
				out = append(out, map[string]any{"push_token": tok, "device_type": "android"})
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(out)
		case "/api/v0/node/inbox-item":
			if r.Header.Get("X-API-Key") != "node:key" {
				w.WriteHeader(401)
				return
			}
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			f.posted = in
			f.mu.Unlock()
			title, _ := in["title"].(string)
			_ = os.WriteFile(filepath.Join(f.dir, "shade.xml"), []byte(strings.Replace(shadeXML, "%s", title, 1)), 0o644)
			_ = os.WriteFile(filepath.Join(f.dir, "opened.xml"), []byte(strings.Replace(openedXML, "%s", title, 1)), 0o644)
			_ = os.WriteFile(filepath.Join(f.dir, "title"), []byte(title), 0o644)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "item-1", "sent": true})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *pushFakes) config(t *testing.T) *pushConfig {
	login := filepath.Join(f.dir, "login.yaml")
	write(t, login, "appId: com.jarvis.nodemobile\n---\n- tapOn: Sign in\n")
	return &pushConfig{
		config:    config{out: filepath.Join(f.dir, "out", "mobile-push"), maestro: f.maestro, adb: f.adb, serial: "emulator-5590", adbTimeout: 5 * time.Second, env: []string{"EMAIL=a@b"}},
		loginFlow: login, notifications: f.srv.URL, cc: f.srv.URL, userToken: "user-jwt", nodeKey: "node:key", userID: 7,
		tokenWait: 2 * time.Second, shadeWait: 2 * time.Second, openWait: 2 * time.Second, poll: 10 * time.Millisecond,
	}
}

func readResults(t *testing.T, dir string) Results {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Results
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRealPushPasses(t *testing.T) {
	f := newPushFakes(t, true)
	pc := f.config(t)
	res, err := realPush(context.Background(), pc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "passed" || len(res.Tests) != 1 || res.Tests[0].Status != "passed" {
		t.Fatalf("results = %+v", res)
	}
	r := readResults(t, pc.out)
	if r.Suite != "mobile-push" || r.Tests[0].Suite != "mobile-push" {
		t.Errorf("suite = %q / %q", r.Suite, r.Tests[0].Suite)
	}
	if len(r.Tests[0].Screenshots) != 2 {
		t.Errorf("screenshots = %v, want the shade and the opened screen", r.Tests[0].Screenshots)
	}
	calls, _ := os.ReadFile(f.calls)
	log := string(calls)
	for _, want := range []string{"maestro --device emulator-5590 test", "-e EMAIL=a@b", "shell input keyevent KEYCODE_HOME",
		"shell cmd statusbar expand-notifications", "shell input tap 500 330"} {
		if !strings.Contains(log, want) {
			t.Errorf("calls lack %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "emulator-5554") {
		t.Error("touched the user's emulator")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posted["create_push_notification"] != true || f.posted["user_id"] != float64(7) || f.posted["target_type"] != "user" {
		t.Errorf("event posted = %v", f.posted)
	}
}

func TestRealPushFailsWhenTheAppRegistersNoToken(t *testing.T) {
	f := newPushFakes(t, true)
	f.tokens = nil
	pc := f.config(t)
	res, err := realPush(context.Background(), pc)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Tests[0]
	if r.Status != "failed" || r.Error == nil || !strings.Contains(*r.Error, "push token") {
		t.Fatalf("record = %+v", r)
	}
}

func TestRealPushFailsWhenTheTapOpensNothing(t *testing.T) {
	f := newPushFakes(t, false)
	res, err := realPush(context.Background(), f.config(t))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Tests[0]
	if r.Status != "failed" || r.Error == nil || !strings.Contains(*r.Error, "did not open") {
		t.Fatalf("record = %+v", r)
	}
	if len(r.Attachments) == 0 {
		t.Error("a failure keeps the last window dump")
	}
}

func TestRealPushSkipsWithItsReason(t *testing.T) {
	f := newPushFakes(t, true)
	pc := f.config(t)
	pc.skipReason = "no relay: set E2E_REAL_RELAY_URL"
	res, err := realPush(context.Background(), pc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "passed" || res.Tests[0].Status != "skipped" || res.Tests[0].SkipReason != pc.skipReason {
		t.Fatalf("results = %+v", res)
	}
	if calls, _ := os.ReadFile(f.calls); len(calls) != 0 {
		t.Errorf("a skipped lane ran something: %s", calls)
	}
	if readResults(t, pc.out).Tests[0].SkipReason != pc.skipReason {
		t.Error("results.json lacks the reason")
	}
}

func TestRealPushRefusesTheUsersEmulator(t *testing.T) {
	f := newPushFakes(t, true)
	pc := f.config(t)
	pc.serial = "emulator-5554"
	if _, err := realPush(context.Background(), pc); err == nil {
		t.Fatal("ran on emulator-5554")
	}
}

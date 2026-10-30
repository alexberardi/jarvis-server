package nodesettings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fakeCC is command-center's node routes in memory: it records every call and answers from
// its fields.
type fakeCC struct {
	mu       sync.Mutex
	calls    []string
	bodies   map[string]any
	k2       map[string]any // GET k2/provision/{rid}; nil is a 404
	requests []map[string]any
	confirm  int // status of GET settings/requests/{rid}; 0 is 200
	pending  []map[string]any
	pkgSecrets map[string]string // GET package-secrets; nil is a 404 (an older jarvisd)
}

func (f *fakeCC) Do(_ context.Context, method, path string, body, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := method + " " + path
	f.calls = append(f.calls, call)
	if f.bodies == nil {
		f.bodies = map[string]any{}
	}
	f.bodies[call] = body
	answer := func(v any) error {
		if out == nil {
			return nil
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	switch {
	case method == http.MethodGet && strings.Contains(path, "/k2/provision/"):
		if f.k2 == nil {
			return &node.HTTPError{Method: method, Path: path, Status: 404, Detail: "No pending K2 for this request"}
		}
		return answer(f.k2)
	case method == http.MethodGet && strings.HasSuffix(path, "/settings/requests"):
		return answer(f.requests)
	case method == http.MethodGet && strings.Contains(path, "/settings/requests/"):
		if f.confirm != 0 {
			return &node.HTTPError{Method: method, Path: path, Status: f.confirm}
		}
		return answer(map[string]any{"status": "pending"})
	case method == http.MethodGet && strings.HasSuffix(path, "/config/pending"):
		return answer(f.pending)
	case method == http.MethodGet && strings.HasSuffix(path, "/package-secrets"):
		if f.pkgSecrets == nil {
			return &node.HTTPError{Method: method, Path: path, Status: 404}
		}
		return answer(map[string]any{"secrets": f.pkgSecrets})
	}
	return nil
}

func (f *fakeCC) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (f *fakeCC) body(call string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(f.bodies[call])
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newHandler(t *testing.T, nodeID string) (*Handler, *fakeCC, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := OpenStore(dir, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeCC{}
	return &Handler{NodeID: nodeID, API: f, Store: st, Log: quiet()}, f, dir
}

var testKey = b64([]byte("0123456789abcdef0123456789abcdef"))

func TestK2ProvisionPullsTheKeyAndAcks(t *testing.T) {
	h, f, dir := newHandler(t, "n1")
	changed := 0
	h.KeyChanged = func() { changed++ }
	f.k2 = map[string]any{"k2": testKey, "kid": "k2-2026-10", "created_at": "2026-10-14T00:00:00Z"}
	h.Handle(context.Background(), TopicK2Provision, []byte(`{"request_id":"r1"}`))
	if !f.called("GET /api/v0/nodes/n1/k2/provision/r1") {
		t.Fatalf("calls = %v", f.calls)
	}
	if ack := f.body("POST /api/v0/nodes/n1/k2/ack/r1"); ack["success"] != true {
		t.Fatalf("ack = %v", ack)
	}
	if !h.Store.HasK2() || changed != 1 {
		t.Fatalf("stored %v, changed %d", h.Store.HasK2(), changed)
	}
	// Persisted, 0600, and read back by a restart.
	if st, _ := os.Stat(filepath.Join(dir, K2File)); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("k2.json mode %o", st.Mode().Perm())
	}
	again, err := OpenStore(dir, "n1")
	if err != nil || !again.HasK2() {
		t.Fatalf("reopen: %v %v", again, err)
	}
}

// A spoofed nudge (nothing pending at command-center) stores nothing and acks a failure.
func TestK2ProvisionRefusesASpoofedNudge(t *testing.T) {
	h, f, _ := newHandler(t, "n1")
	h.Handle(context.Background(), TopicK2Provision, []byte(`{"request_id":"forged"}`))
	ack := f.body("POST /api/v0/nodes/n1/k2/ack/forged")
	if ack["success"] != false || ack["error"] != "verification failed" || h.Store.HasK2() {
		t.Fatalf("ack = %v, stored = %v", ack, h.Store.HasK2())
	}
	// A short key is refused too.
	f.k2 = map[string]any{"k2": b64([]byte("short")), "kid": "k"}
	h.Handle(context.Background(), TopicK2Provision, []byte(`{"request_id":"r2"}`))
	if ack := f.body("POST /api/v0/nodes/n1/k2/ack/r2"); ack["success"] != false || h.Store.HasK2() {
		t.Fatalf("short key: ack = %v", ack)
	}
}

func decryptUpload(t *testing.T, f *fakeCC, h *Handler, rid string) map[string]any {
	t.Helper()
	up := f.body("PUT /api/v0/nodes/" + h.NodeID + "/settings/requests/" + rid + "/snapshot")
	if up == nil {
		t.Fatalf("no snapshot uploaded; calls %v", f.calls)
	}
	if up["aad_schema_version"] != 1.0 || up["aad_commands_schema_version"] != 2.0 || up["aad_revision"] != 1.0 {
		t.Fatalf("aad fields = %v", up)
	}
	pt, err := Open(h.Store.Key(), h.NodeID, SnapshotConfigType,
		Sealed{Ciphertext: up["ciphertext"].(string), Nonce: up["nonce"].(string), Tag: up["tag"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	var snap map[string]any
	if err := json.Unmarshal(pt, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func secretsOf(snap map[string]any, cmd string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, c := range snap["commands"].([]any) {
		c := c.(map[string]any)
		if c["command_name"] != cmd {
			continue
		}
		for _, s := range c["secrets"].([]any) {
			s := s.(map[string]any)
			out[s["key"].(string)] = s
		}
	}
	return out
}

var weatherCmds = []Command{
	{Name: "desktop_open", Description: "Open a link or an app"},
	{Name: "get_weather", Description: "Weather", Secrets: []SecretSpec{
		{Key: "WEATHER_API_KEY", Scope: "integration", Description: "API key", ValueType: "string", Required: true, IsSensitive: true},
		{Key: "UNITS", Scope: "integration", Description: "Units", ValueType: "string", EnumValues: []string{"metric", "imperial"}},
		{Key: "CITY", Scope: "user", Description: "Your city", ValueType: "string", FriendlyName: "City"},
	}},
}

func TestSettingsRequestUploadsAnEncryptedSnapshot(t *testing.T) {
	h, f, _ := newHandler(t, "n1")
	h.Commands = func(context.Context) []Command { return weatherCmds }
	ctx := context.Background()
	// No K2: nothing is uploaded (the app offers "Generate Encryption Key").
	h.Handle(ctx, TopicSettingsRequest, []byte(`{"request_id":"r0","node_id":"n1"}`))
	if len(f.calls) != 0 {
		t.Fatalf("calls without K2: %v", f.calls)
	}
	if err := h.Store.SaveK2(testKey, "kid", ""); err != nil {
		t.Fatal(err)
	}
	seven := int64(7)
	_ = h.Store.SetSecret("WEATHER_API_KEY", "s3cret", "integration", "", nil)
	_ = h.Store.SetSecret("UNITS", "metric", "integration", "", nil)
	_ = h.Store.SetSecret("CITY", "Boston", "user", "", &seven)
	_ = h.Store.SetEnabled("desktop_open", false)

	h.Handle(ctx, TopicSettingsRequest, []byte(`{"request_id":"r1","node_id":"n1","user_id":7}`))
	if !f.called("GET /api/v0/nodes/n1/settings/requests/r1") {
		t.Fatal("request not confirmed")
	}
	snap := decryptUpload(t, f, h, "r1")
	if snap["schema_version"] != 1.0 || snap["commands_schema_version"] != 2.0 || len(snap["agents"].([]any)) != 0 {
		t.Fatalf("snapshot = %v", snap)
	}
	nc := snap["node_config"].(map[string]any)
	hw := nc["hardware"].(map[string]any)
	if len(nc) != 1 || len(hw) != 3 || hw["hat_detected"] != false || hw["led_chain_available"] != false || hw["button_available"] != false {
		t.Fatalf("node_config = %v", nc)
	}
	s := secretsOf(snap, "get_weather")
	if s["WEATHER_API_KEY"]["is_set"] != true || s["WEATHER_API_KEY"]["value"] != nil {
		t.Errorf("sensitive secret leaked or unset: %v", s["WEATHER_API_KEY"])
	}
	if s["UNITS"]["value"] != "metric" || s["UNITS"]["enum_values"] == nil {
		t.Errorf("non-sensitive secret: %v", s["UNITS"])
	}
	if s["CITY"]["value"] != "Boston" || s["CITY"]["is_set"] != true || s["CITY"]["friendly_name"] != "City" {
		t.Errorf("user secret for user 7: %v", s["CITY"])
	}
	for _, c := range snap["commands"].([]any) {
		c := c.(map[string]any)
		if want := c["command_name"] != "desktop_open"; c["enabled"] != want {
			t.Errorf("%v enabled = %v", c["command_name"], c["enabled"])
		}
	}

	// include_values (secret sync) carries sensitive values; another user doesn't see user 7's.
	h.Handle(ctx, TopicSettingsRequest, []byte(`{"request_id":"r2","include_values":true,"user_id":8}`))
	s = secretsOf(decryptUpload(t, f, h, "r2"), "get_weather")
	if s["WEATHER_API_KEY"]["value"] != "s3cret" || s["CITY"]["is_set"] != false {
		t.Errorf("include_values for user 8: %v", s)
	}

	// An expired request (410 on confirm) gets no upload.
	f.confirm = http.StatusGone
	h.Handle(ctx, TopicSettingsRequest, []byte(`{"request_id":"r3"}`))
	if f.called("PUT /api/v0/nodes/n1/settings/requests/r3/snapshot") {
		t.Fatal("uploaded for an expired request")
	}
}

// Config pushes exactly as the mobile app makes them (the Python-checked goldens).
func TestConfigPushAppliesGoldenPushesAndAcks(t *testing.T) {
	g := loadGolden(t)
	h, f, _ := newHandler(t, g.NodeID)
	if err := h.Store.SaveK2(g.K2, "kid", ""); err != nil {
		t.Fatal(err)
	}
	for i, p := range g.Pushes {
		f.pending = append(f.pending, map[string]any{"id": "p" + string(rune('0'+i)), "config_type": p.ConfigType,
			"ciphertext": p.Ciphertext, "nonce": p.Nonce, "tag": p.Tag})
	}
	// A push sealed for another node is left pending (not acked).
	f.pending = append(f.pending, map[string]any{"id": "bad", "config_type": "settings:secrets",
		"ciphertext": g.Pushes[0].Ciphertext, "nonce": g.Pushes[0].Nonce, "tag": g.Pushes[1].Tag})
	changed := 0
	h.SecretsChanged = func() { changed++ }

	h.Handle(context.Background(), TopicConfigPush, []byte(`{"push_id":"p0","config_type":"settings:secrets"}`))
	for i := range g.Pushes {
		if !f.called("POST /api/v0/nodes/" + g.NodeID + "/config/p" + string(rune('0'+i)) + "/ack") {
			t.Errorf("push %d (%s) not acked", i, g.Pushes[i].ConfigType)
		}
	}
	if f.called("POST /api/v0/nodes/" + g.NodeID + "/config/bad/ack") {
		t.Error("an undecryptable push was acked")
	}
	seven := int64(7)
	st := h.Store
	// Push 0 set WEATHER_API_KEY and UNITS, push 2 cleared WEATHER_API_KEY again.
	if st.Secret("WEATHER_API_KEY", "integration", nil) != "" || st.Secret("UNITS", "integration", nil) != "metric" {
		t.Errorf("integration secrets: key %q units %q", st.Secret("WEATHER_API_KEY", "integration", nil), st.Secret("UNITS", "integration", nil))
	}
	if st.Secret("EMAIL_PASSWORD", "user", &seven) != "pä55 wörd" || st.Secret("EMAIL_PASSWORD", "integration", nil) != "" {
		t.Error("user secret not stored for user 7 only")
	}
	if st.Enabled("get_weather") {
		t.Error("command_registry push didn't disable get_weather")
	}
	// Each applied secrets push (three of them) re-reads what depends on secrets.
	if changed != 3 {
		t.Errorf("SecretsChanged ran %d times, want 3", changed)
	}
}

func TestBackstopKeepsIncludeValuesAndUser(t *testing.T) {
	h, f, _ := newHandler(t, "n1")
	h.Commands = func(context.Context) []Command { return weatherCmds }
	_ = h.Store.SaveK2(testKey, "kid", "")
	seven := int64(7)
	_ = h.Store.SetSecret("WEATHER_API_KEY", "s3cret", "integration", "", nil)
	_ = h.Store.SetSecret("CITY", "Boston", "user", "", &seven)
	f.requests = []map[string]any{{"request_id": "r9", "include_values": true, "user_id": 7}}
	h.Backstop(context.Background())
	s := secretsOf(decryptUpload(t, f, h, "r9"), "get_weather")
	if s["WEATHER_API_KEY"]["value"] != "s3cret" || s["CITY"]["value"] != "Boston" {
		t.Errorf("backstop snapshot = %v", s)
	}
	if !f.called("GET /api/v0/nodes/n1/config/pending") {
		t.Error("backstop didn't look for config pushes")
	}
}

// A new sign-in (another node id) never inherits the previous node's key, secrets or data.
func TestOpenWipesAnotherNodesFiles(t *testing.T) {
	dir := t.TempDir()
	old, _ := OpenStore(dir, "old")
	_ = old.SaveK2(testKey, "kid", "")
	_ = old.SetSecret("K", "v", "integration", "", nil)
	_ = old.SaveData("notes", "a", obj(t, `{"x":1}`), nil)
	if again, _ := OpenStore(dir, "old"); !again.HasK2() || again.Secret("K", "integration", nil) != "v" {
		t.Fatal("same node lost its settings")
	}
	st, err := OpenStore(dir, "new")
	if err != nil {
		t.Fatal(err)
	}
	if st.HasK2() || st.Secret("K", "integration", nil) != "" || len(st.Records("notes", time.Now())) != 0 {
		t.Fatal("new node inherited the old node's settings")
	}
	for _, f := range []string{K2File, SettingsFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s still on disk: %v", f, err)
		}
	}
}

// Records keep their keys' order and their place (first save) through the file, as the Pi's
// command_data rows do; an update keeps the expiry; expired rows are gone.
func TestCommandDataStore(t *testing.T) {
	dir := t.TempDir()
	st, _ := OpenStore(dir, "n1")
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	_ = st.SaveData("notes", "b", obj(t, `{"z":"B","a":1}`), &future)
	_ = st.SaveData("notes", "a", obj(t, `{"v":"A","big":12345678901234567890}`), nil)
	_ = st.SaveData("notes", "old", obj(t, `{"v":"gone"}`), &past)
	_ = st.SaveData("notes", "b", obj(t, `{"z":"B2","a":2}`), &future) // upsert: keeps its place
	st, _ = OpenStore(dir, "n1")
	if _, ok := st.Data("notes", "old", now); ok {
		t.Fatal("expired record returned")
	}
	if d, _ := st.Data("notes", "a", now); string(must(d.MarshalJSON())) != `{"v":"A","big":12345678901234567890}` {
		t.Fatalf("get = %s", must(d.MarshalJSON()))
	}
	all := st.Records("notes", now)
	if len(all) != 2 || all[0].Key != "b" || all[1].Key != "a" || string(must(all[0].Data.MarshalJSON())) != `{"z":"B2","a":2}` {
		t.Fatalf("records = %+v", all)
	}
	merged, found, err := st.UpdateData("notes", "b", obj(t, `{"a":3,"n":"new"}`), now, func(Object) bool { return true })
	if err != nil || !found || string(must(merged.MarshalJSON())) != `{"z":"B2","a":3,"n":"new"}` {
		t.Fatalf("update = %s, %v, %v", must(merged.MarshalJSON()), found, err)
	}
	if r := st.Records("notes", now); r[0].ExpiresAt == nil || !r[0].ExpiresAt.Equal(future) {
		t.Fatalf("update dropped the expiry: %+v", r[0])
	}
	if _, found, _ := st.UpdateData("notes", "b", obj(t, `{"a":4}`), now, func(Object) bool { return false }); found {
		t.Fatal("refused update applied")
	}
	if _, found, _ := st.UpdateData("notes", "old", obj(t, `{"a":4}`), now, func(Object) bool { return true }); found {
		t.Fatal("expired record updated")
	}
	if ok, _ := st.DeleteData("notes", "a"); !ok {
		t.Fatal("delete")
	}
	if n, _ := st.DeleteAllData("notes"); n != 2 {
		t.Fatalf("delete all = %d", n)
	}
}

func obj(t *testing.T, s string) Object {
	t.Helper()
	var o Object
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

type fakeTools []string

func (f fakeTools) Available(context.Context) []node.ToolDef {
	var out []node.ToolDef
	for _, n := range f {
		out = append(out, node.ToolDef{Name: n})
	}
	return out
}

func (f fakeTools) Call(_ context.Context, name string, _ map[string]any, _ node.Request) (map[string]any, bool) {
	return map[string]any{"success": true, "ran": name}, true
}

func TestGateHidesDisabledCommands(t *testing.T) {
	st, _ := OpenStore(t.TempDir(), "n1")
	g := Gate{Tools: fakeTools{"a", "b"}, Store: st}
	_ = st.SetEnabled("a", false)
	if av := g.Available(context.Background()); len(av) != 1 || av[0].Name != "b" {
		t.Fatalf("available = %v", av)
	}
	if out, ok := g.Call(context.Background(), "a", nil, node.Request{}); !ok || out["success"] != false {
		t.Fatalf("disabled call = %v", out)
	}
	_ = st.SetEnabled("a", true)
	if out, _ := g.Call(context.Background(), "a", nil, node.Request{}); out["ran"] != "a" {
		t.Fatalf("re-enabled call = %v", out)
	}
}

// A Pi's node_config (Handler.NodeConfig) goes in the snapshot as given.
func TestSnapshotCarriesTheNodeConfig(t *testing.T) {
	h, f, _ := newHandler(t, "n1")
	h.Commands = func(context.Context) []Command { return nil }
	h.NodeConfig = func() map[string]any {
		return map[string]any{"wake_word_threshold": 0.5, "hardware": map[string]any{"hat_detected": true}}
	}
	if err := h.Store.SaveK2(testKey, "kid", ""); err != nil {
		t.Fatal(err)
	}
	h.Handle(context.Background(), TopicSettingsRequest, []byte(`{"request_id":"r1","node_id":"n1"}`))
	nc := decryptUpload(t, f, h, "r1")["node_config"].(map[string]any)
	if nc["wake_word_threshold"] != 0.5 || nc["hardware"].(map[string]any)["hat_detected"] != true {
		t.Fatalf("node_config = %v", nc)
	}
}

// A command's parameters go in the snapshot, so the app's routine editor builds a step's fields
// (and its device pickers) from them; a command without any has no "parameters" key, as on N:.
func TestSnapshotCarriesCommandParameters(t *testing.T) {
	h, f, _ := newHandler(t, "n1")
	params := []any{map[string]any{"name": "device_name", "type": "string", "required": true, "options_source": "devices"}}
	h.Commands = func(context.Context) []Command {
		return []Command{{Name: "control_device", Parameters: params}, {Name: "tell_joke"}}
	}
	if err := h.Store.SaveK2(testKey, "kid", ""); err != nil {
		t.Fatal(err)
	}
	h.Handle(context.Background(), TopicSettingsRequest, []byte(`{"request_id":"r1","node_id":"n1"}`))
	cmds := decryptUpload(t, f, h, "r1")["commands"].([]any)
	cd, joke := cmds[0].(map[string]any), cmds[1].(map[string]any)
	got, _ := cd["parameters"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["options_source"] != "devices" {
		t.Fatalf("control_device parameters = %v", cd["parameters"])
	}
	if _, ok := joke["parameters"]; ok {
		t.Fatalf("tell_joke has parameters %v", joke["parameters"])
	}
}

// The sidecar's device families and managers go in the snapshot as N: builds them, their
// secrets read from this store: a family's sensitive value only with include_values, a
// manager's never (N:'s honor_include_values=False).
func TestSnapshotCarriesDeviceFamiliesAndManagers(t *testing.T) {
	h, f, _ := newHandler(t, "n1")
	h.Commands = func(context.Context) []Command { return nil }
	secret := []SecretSpec{{Key: "GOVEE_KEY", Scope: "integration", ValueType: "string", Required: true, IsSensitive: true}}
	h.Devices = func(context.Context) Devices {
		return Devices{
			Families: []DeviceComponent{{Entry: map[string]any{"family_name": "govee", "is_configured": true}, Secrets: secret}},
			Managers: []DeviceComponent{{Entry: map[string]any{"manager_name": "jarvis_direct"}, Secrets: secret}},
		}
	}
	if err := h.Store.SaveK2(testKey, "kid", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.SetSecret("GOVEE_KEY", "k-123", "integration", "string", nil); err != nil {
		t.Fatal(err)
	}
	h.Handle(context.Background(), TopicSettingsRequest, []byte(`{"request_id":"r1","node_id":"n1","include_values":true}`))
	snap := decryptUpload(t, f, h, "r1")
	fams := snap["device_families"].([]any)
	if len(fams) != 1 {
		t.Fatalf("device_families = %v", fams)
	}
	fam := fams[0].(map[string]any)
	fs := fam["secrets"].([]any)[0].(map[string]any)
	if fam["family_name"] != "govee" || fam["is_configured"] != true || fs["is_set"] != true || fs["value"] != "k-123" {
		t.Fatalf("family = %v", fam)
	}
	mgr := snap["device_managers"].([]any)[0].(map[string]any)
	ms := mgr["secrets"].([]any)[0].(map[string]any)
	if mgr["manager_name"] != "jarvis_direct" || ms["is_set"] != true || ms["value"] != nil {
		t.Fatalf("manager = %v", mgr)
	}
}

// A sealed store (a factory reset is wiping it) refuses every write and never writes a file
// back.
func TestSealedStoreNeverWrites(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenStore(dir, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEnabled("weather", false); err != nil {
		t.Fatal(err)
	}
	st.Seal()
	if err := os.Remove(filepath.Join(dir, SettingsFile)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEnabled("weather", true); !errors.Is(err, ErrSealed) {
		t.Fatalf("SetEnabled after Seal = %v, want ErrSealed", err)
	}
	if err := st.SaveData("weather", "k", obj(t, `{"a":1}`), nil); !errors.Is(err, ErrSealed) {
		t.Fatalf("SaveData after Seal = %v, want ErrSealed", err)
	}
	if err := st.SaveK2(testKey, "kid", "2026-10-14T00:00:00Z"); !errors.Is(err, ErrSealed) {
		t.Fatalf("SaveK2 after Seal = %v, want ErrSealed", err)
	}
	for _, f := range []string{SettingsFile, K2File} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Fatalf("%s written after Seal", f)
		}
	}
}

// directTools also carries out commands itself and answers lists (funnel stage 1).
type directTools struct{ fakeTools }

func (directTools) Direct(ctx context.Context, _ string) (map[string]any, bool) {
	if !node.Enabled(ctx, "desktop_view") {
		return nil, false
	}
	return map[string]any{"success": true, "direct": true}, true
}
func (directTools) AnswerChoice(context.Context, string) (map[string]any, bool) {
	return map[string]any{"success": true, "answered": true}, true
}

// The gate wraps the voice loop's tools: stage 1 and list answers must reach them (2026-10-25:
// they didn't, and every "switch to X" and "the second one" went to the model).
func TestGatePassesDirectAndChoiceAnswers(t *testing.T) {
	st, _ := OpenStore(t.TempDir(), "n1")
	var ts node.Toolset = Gate{Tools: directTools{fakeTools{"desktop_view", "desktop_window"}}, Store: st}
	ctx := context.Background()
	if out, ok := ts.(node.Director).Direct(ctx, "switch to the Claude tab"); !ok || out["direct"] != true {
		t.Fatalf("direct = %v %v", out, ok)
	}
	if out, ok := ts.(node.ChoiceAnswerer).AnswerChoice(ctx, "the second one"); !ok || out["answered"] != true {
		t.Fatalf("answer = %v %v", out, ok)
	}
	_ = st.SetEnabled("desktop_view", false)
	if _, ok := ts.(node.Director).Direct(ctx, "switch to the Claude tab"); ok {
		t.Fatal("stage 1 didn't see the command turned off")
	}
}

// The app's Inspect fast-paths screen switches one pattern: the push is kept (sparse, on by
// default), survives a reopen, and the snapshot reports each pattern's state.
func TestFastPathSwitches(t *testing.T) {
	h, f, dir := newHandler(t, "n1")
	if err := h.apply("fast_path_registry", map[string]any{"command_name": "routine", "pattern_id": "routine.good_night", "enabled": "false"}); err != nil {
		t.Fatal(err)
	}
	if h.Store.FastPathEnabled("routine", "routine.good_night") || !h.Store.FastPathEnabled("routine", "routine.good_morning") {
		t.Fatal("switch not applied to just that pattern")
	}
	again, err := OpenStore(dir, "n1")
	if err != nil || again.FastPathEnabled("routine", "routine.good_night") {
		t.Fatalf("switch not kept: %v", err)
	}
	// A push without a pattern changes nothing.
	if err := h.apply("fast_path_registry", map[string]any{"command_name": "routine"}); err != nil {
		t.Fatal(err)
	}

	h.Commands = func(context.Context) []Command {
		return []Command{{Name: "routine", FastPaths: []FastPath{
			{ID: "routine.good_morning", Description: "m", Example: "good morning"},
			{ID: "routine.good_night", Description: "n", Example: "good night"},
		}}}
	}
	if err := h.Store.SaveK2(testKey, "kid", ""); err != nil {
		t.Fatal(err)
	}
	h.Handle(context.Background(), TopicSettingsRequest, []byte(`{"request_id":"r1","node_id":"n1"}`))
	fps := decryptUpload(t, f, h, "r1")["commands"].([]any)[0].(map[string]any)["fast_paths"].([]any)
	if len(fps) != 2 || fps[0].(map[string]any)["enabled"] != true || fps[1].(map[string]any)["enabled"] != false ||
		fps[1].(map[string]any)["example"] != "good night" {
		t.Fatalf("fast_paths = %v", fps)
	}

	if err := h.apply("fast_path_registry", map[string]any{"command_name": "routine", "pattern_id": "routine.good_night", "enabled": "true"}); err != nil {
		t.Fatal(err)
	}
	if !h.Store.FastPathEnabled("routine", "routine.good_night") {
		t.Fatal("switched back on: still off")
	}
}

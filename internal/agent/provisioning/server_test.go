package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fakeWiFi records calls; connectErr/apErr make them fail, and block (when set) holds Connect
// until it is closed.
type fakeWiFi struct {
	mu         sync.Mutex
	calls      []string
	connectErr error
	apErr      error
	block      chan struct{}
}

func (f *fakeWiFi) note(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeWiFi) Calls() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

func (f *fakeWiFi) ScanNetworks(context.Context) ([]NetworkInfo, error) {
	return []NetworkInfo{{SSID: "HomeNetwork", SignalStrength: -45, Security: "WPA2"}}, nil
}
func (f *fakeWiFi) Connect(_ context.Context, ssid, pw string) error {
	if f.block != nil {
		<-f.block
	}
	f.note("connect " + ssid + " " + pw)
	return f.connectErr
}
func (f *fakeWiFi) CommitConnection(context.Context) error   { f.note("commit"); return nil }
func (f *fakeWiFi) RollbackConnection(context.Context) error { f.note("rollback"); return nil }
func (f *fakeWiFi) StartAPMode(_ context.Context, ssid string) error {
	f.note("ap " + ssid)
	return f.apErr
}
func (f *fakeWiFi) StopAPMode(context.Context) error            { f.note("ap down"); return nil }
func (f *fakeWiFi) CurrentSSID(context.Context) (string, error) { return "", nil }

// fakeCC answers /api/v0/nodes/register with status (0 means 200 with a key) and counts calls.
type fakeCC struct {
	*httptest.Server
	status atomic.Int32
	calls  atomic.Int32
	body   atomic.Value
}

func newFakeCC(t *testing.T) *fakeCC {
	cc := &fakeCC{}
	cc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cc.calls.Add(1)
		if r.URL.Path != "/api/v0/nodes/register" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		cc.body.Store(string(b))
		w.Header().Set("Content-Type", "application/json")
		switch st := int(cc.status.Load()); st {
		case 0:
			io.WriteString(w, `{"node_id":"cc-node-1","room":"kitchen","node_key":"secret-key"}`)
		case 200:
			io.WriteString(w, `{"node_id":"cc-node-1"}`)
		default:
			w.WriteHeader(st)
			io.WriteString(w, `{"detail":"Invalid or expired provisioning token"}`)
		}
	}))
	t.Cleanup(cc.Close)
	return cc
}

type rig struct {
	t        *testing.T
	srv      *Server
	http     *httptest.Server
	wifi     *fakeWiFi
	cc       *fakeCC
	creds    []node.Creds
	k2       []string
	marked   atomic.Bool
	provised atomic.Int32
}

func newRig(t *testing.T, mod func(*Options)) *rig {
	r := &rig{t: t, wifi: &fakeWiFi{}, cc: newFakeCC(t)}
	o := Options{
		WiFi: r.wifi, APSSID: "Jarvis-Setup-1234", FirmwareVersion: "0.9.0",
		Identity: func() Identity {
			return Identity{NodeID: "jarvis-a1b2c3d4", Hardware: "pi-zero-w", MAC: "b8:27:eb:a1:b2:c3"}
		},
		SaveCreds: func(c node.Creds) error { r.creds = append(r.creds, c); return nil },
		SaveK2: func(id, k2, kid, at string) error {
			r.k2 = append(r.k2, id+" "+kid+" "+at)
			return nil
		},
		Provisioned:     r.marked.Load,
		MarkProvisioned: func() error { r.marked.Store(true); return nil },
		OnProvisioned:   func() { r.provised.Add(1) },
	}
	if mod != nil {
		mod(&o)
	}
	r.srv = New(context.Background(), o)
	r.srv.retry, r.srv.grace = 0, 0
	r.http = httptest.NewServer(r.srv)
	t.Cleanup(r.http.Close)
	return r
}

func (r *rig) do(method, path, body string) (int, string) {
	r.t.Helper()
	req, _ := http.NewRequest(method, r.http.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (r *rig) provisionBody() string {
	b, _ := json.Marshal(map[string]any{
		"wifi_ssid": "HomeNetwork", "wifi_password": "hunter22", "room": "kitchen",
		"command_center_url": r.cc.URL, "config_service_url": "http://192.0.2.10:7700",
		"household_id": "hh-1", "node_id": "cc-node-1", "provisioning_token": "tok-1",
	})
	return string(b)
}

// provision starts an attempt, waits for it and returns the final status.
func (r *rig) provision() Status {
	r.t.Helper()
	code, body := r.do("POST", "/api/v1/provision", r.provisionBody())
	if code != 200 || body != `{"success":true,"message":"Credentials received. Attempting connection..."}` {
		r.t.Fatalf("provision: %d %s", code, body)
	}
	r.srv.wg.Wait()
	return r.status()
}

func (r *rig) status() Status {
	r.t.Helper()
	_, body := r.do("GET", "/api/v1/status", "")
	var st Status
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		r.t.Fatal(body, err)
	}
	return st
}

func TestProvisionHappyPath(t *testing.T) {
	r := newRig(t, nil)
	st := r.provision()
	if st.State != StateProvisioned || st.ProgressPercent != 100 || st.Error != nil || st.Message != msgProvisioned {
		t.Fatalf("status %+v", st)
	}
	if got := r.wifi.Calls(); got != "connect HomeNetwork hunter22,commit" {
		t.Errorf("wifi calls %q", got)
	}
	if got, want := r.cc.body.Load(), `{"node_id":"cc-node-1","provisioning_token":"tok-1","room":"kitchen"}`; got != want {
		t.Errorf("register body %s, want %s", got, want)
	}
	if len(r.creds) != 1 {
		t.Fatalf("creds %v", r.creds)
	}
	c := r.creds[0]
	if c.NodeID != "cc-node-1" || c.NodeKey != "secret-key" || c.ConfigURL != "http://192.0.2.10:7700" || c.Room != "kitchen" {
		t.Errorf("creds %+v", c)
	}
	if !r.marked.Load() || r.provised.Load() != 1 {
		t.Errorf("marked %v, on_provisioned %d", r.marked.Load(), r.provised.Load())
	}
	_, info := r.do("GET", "/api/v1/info", "")
	if want := `{"node_id":"jarvis-a1b2c3d4","firmware_version":"0.9.0","hardware":"pi-zero-w","mac_address":"b8:27:eb:a1:b2:c3","capabilities":["voice","speaker"],"state":"PROVISIONED","previously_provisioned":true}`; info != want {
		t.Errorf("info %s", info)
	}
}

func TestProvisionWiFiFailureRestoresHotspot(t *testing.T) {
	r := newRig(t, nil)
	r.wifi.connectErr = errors.New("no such network")
	st := r.provision()
	if st.State != StateError || *st.ErrorCode != ErrWiFiConnect || *st.Error != "Failed to connect to HomeNetwork" ||
		!st.Retryable || !st.HotspotRestored || st.Message != msgFailedRestored || st.RegistrationStatus != nil || st.ProgressPercent != 35 {
		t.Fatalf("status %+v", st)
	}
	if got := r.wifi.Calls(); got != "connect HomeNetwork hunter22,rollback,ap Jarvis-Setup-1234" {
		t.Errorf("wifi calls %q", got)
	}
	if r.cc.calls.Load() != 0 || len(r.creds) != 0 || r.marked.Load() {
		t.Error("a failed join registered or marked the node")
	}
	// The app may resend: the lock was released.
	r.wifi.connectErr = nil
	if st := r.provision(); st.State != StateProvisioned {
		t.Errorf("retry: %+v", st)
	}
}

func TestProvisionHotspotNotRestoredIsNotRetryable(t *testing.T) {
	r := newRig(t, nil)
	r.wifi.connectErr, r.wifi.apErr = errors.New("x"), errors.New("nm: AP mode failed")
	st := r.provision()
	if st.State != StateError || st.Retryable || st.HotspotRestored || st.Message != msgFailed {
		t.Fatalf("status %+v", st)
	}
	r = newRig(t, func(o *Options) { o.APSSID = "" })
	r.wifi.connectErr = errors.New("x")
	if st := r.provision(); st.Retryable || st.HotspotRestored || r.wifi.Calls() != "connect HomeNetwork hunter22,rollback" {
		t.Fatalf("no ssid: %+v, calls %q", st, r.wifi.Calls())
	}
}

func TestProvisionRegistration4xxNotRetried(t *testing.T) {
	r := newRig(t, nil)
	r.cc.status.Store(401)
	st := r.provision()
	if st.State != StateError || *st.ErrorCode != ErrRegistrationFailed || st.RegistrationStatus == nil || *st.RegistrationStatus != 401 ||
		*st.Error != "Command center rejected registration (HTTP 401): Invalid or expired provisioning token" || !st.HotspotRestored {
		t.Fatalf("status %+v", st)
	}
	if n := r.cc.calls.Load(); n != 1 {
		t.Errorf("%d register calls, want 1", n)
	}
	if got := r.wifi.Calls(); got != "connect HomeNetwork hunter22,rollback,ap Jarvis-Setup-1234" {
		t.Errorf("wifi calls %q", got)
	}
	if len(r.creds) != 0 || r.marked.Load() {
		t.Error("credentials saved after a refusal")
	}
}

func TestProvisionRegistration5xxRetriedThreeTimes(t *testing.T) {
	r := newRig(t, nil)
	r.cc.status.Store(503)
	st := r.provision()
	if n := r.cc.calls.Load(); n != serverErrorRetries {
		t.Errorf("%d register calls, want %d", n, serverErrorRetries)
	}
	if *st.ErrorCode != ErrRegistrationFailed || *st.RegistrationStatus != 503 {
		t.Fatalf("status %+v", st)
	}
}

func TestProvisionRegistrationUnreachableRetried(t *testing.T) {
	r := newRig(t, nil)
	r.cc.Close() // connection refused
	st := r.provision()
	if *st.ErrorCode != ErrRegistrationFailed || st.RegistrationStatus != nil || !strings.HasPrefix(*st.Error, "Could not reach command center: ") {
		t.Fatalf("status %+v", st)
	}
	// The refused connections were retried connectRetries times, then given up on.
	if !st.HotspotRestored {
		t.Error("hotspot not restored")
	}
}

func TestRegisterRetriesNetworkErrors(t *testing.T) {
	cc := newFakeCC(t)
	cc.Close()
	var tries atomic.Int32
	hc := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		tries.Add(1)
		return http.DefaultTransport.RoundTrip(req)
	})}
	reg := register(context.Background(), hc, cc.URL, "", "n", "t", "", 0)
	if reg.kind != kindNetworkError || tries.Load() != connectRetries {
		t.Fatalf("kind %q after %d tries", reg.kind, tries.Load())
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProvisionNoNodeKeyIsBadResponse(t *testing.T) {
	r := newRig(t, nil)
	r.cc.status.Store(200)
	st := r.provision()
	if *st.Error != "Command center did not return node credentials" || st.RegistrationStatus != nil || r.cc.calls.Load() != 1 {
		t.Fatalf("status %+v after %d calls", st, r.cc.calls.Load())
	}
}

func TestProvisionCredentialsAndConfigFailures(t *testing.T) {
	r := newRig(t, func(o *Options) { o.SaveCreds = func(node.Creds) error { return errors.New("disk full") } })
	if st := r.provision(); *st.ErrorCode != ErrCredentialsSave || !st.HotspotRestored || r.marked.Load() {
		t.Fatalf("creds: %+v", st)
	}
	r = newRig(t, func(o *Options) { o.SaveConfig = func(ProvisionRequest) error { return errors.New("ro fs") } })
	st := r.provision()
	// The node never left its hotspot: retryable, nothing to restore.
	if *st.ErrorCode != ErrConfigWrite || !st.Retryable || st.HotspotRestored || r.wifi.Calls() != "" {
		t.Fatalf("config: %+v, calls %q", st, r.wifi.Calls())
	}
}

func TestProvisionPanicIsInternalError(t *testing.T) {
	r := newRig(t, func(o *Options) { o.SaveCreds = func(node.Creds) error { panic("boom") } })
	if st := r.provision(); *st.ErrorCode != ErrInternal || *st.Error != "boom" || !st.HotspotRestored {
		t.Fatalf("status %+v", st)
	}
}

func TestProvisionBusy(t *testing.T) {
	r := newRig(t, nil)
	r.wifi.block = make(chan struct{})
	if code, body := r.do("POST", "/api/v1/provision", r.provisionBody()); code != 200 || !strings.Contains(body, `"success":true`) {
		t.Fatalf("first: %d %s", code, body)
	}
	code, body := r.do("POST", "/api/v1/provision", r.provisionBody())
	if code != 200 || body != `{"success":false,"message":"Provisioning already in progress"}` {
		t.Errorf("second: %d %s", code, body)
	}
	if st := r.status(); st.State != StateConnecting || st.ProgressPercent != 35 || !r.srv.StateMachine().Busy() {
		t.Errorf("mid-attempt status %+v", st)
	}
	close(r.wifi.block)
	r.srv.wg.Wait()
	if st := r.status(); st.State != StateProvisioned {
		t.Errorf("final %+v", st)
	}
}

func TestProvisionValidation(t *testing.T) {
	r := newRig(t, nil)
	for _, body := range []string{
		`not json`,
		`{"wifi_ssid":"x"}`,
		strings.Replace(r.provisionBody(), `"tok-1"`, `null`, 1),
		strings.Replace(r.provisionBody(), `"kitchen"`, `5`, 1),
	} {
		if code, out := r.do("POST", "/api/v1/provision", body); code != 422 || !strings.HasPrefix(out, `{"detail":[`) {
			t.Errorf("%s: %d %s", body, code, out)
		}
	}
	// config_service_url is optional: without it the config service is assumed on CC's host.
	body := strings.Replace(r.provisionBody(), `"config_service_url":"http://192.0.2.10:7700",`, "", 1)
	if code, _ := r.do("POST", "/api/v1/provision", body); code != 200 {
		t.Fatal(code)
	}
	r.srv.wg.Wait()
	if got := r.creds[0].ConfigURL; got != "http://127.0.0.1:7700" {
		t.Errorf("config url %q", got)
	}
}

func TestK2(t *testing.T) {
	r := newRig(t, nil)
	body := `{"node_id":"cc-node-1","kid":"k2-2026-01","k2":"AAAA","created_at":"2026-10-17T09:00:00.000Z"}`
	code, out := r.do("POST", "/api/v1/provision/k2", body)
	if code != 200 || out != `{"success":true,"node_id":"cc-node-1","kid":"k2-2026-01","error":null}` {
		t.Fatalf("%d %s", code, out)
	}
	if len(r.k2) != 1 || r.k2[0] != "cc-node-1 k2-2026-01 2026-10-17T09:00:00.000Z" {
		t.Errorf("saved %v", r.k2)
	}
	if code, _ := r.do("POST", "/api/v1/provision/k2", strings.Replace(body, "2026-10-17T09:00:00.000Z", "yesterday", 1)); code != 422 {
		t.Errorf("bad created_at: %d", code)
	}
	r.marked.Store(true)
	if _, out := r.do("POST", "/api/v1/provision/k2", body); out != `{"success":false,"node_id":null,"kid":null,"error":"Node is already provisioned. Re-enter pairing mode to update K2."}` {
		t.Errorf("provisioned: %s", out)
	}
	r = newRig(t, func(o *Options) {
		o.SaveK2 = func(string, string, string, string) error { return errors.New("k2: not 32 bytes") }
	})
	if _, out := r.do("POST", "/api/v1/provision/k2", body); out != `{"success":false,"node_id":null,"kid":null,"error":"k2: not 32 bytes"}` {
		t.Errorf("invalid: %s", out)
	}
}

func TestFactoryReset(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.FactoryReset = func(context.Context) ([]string, error) { return []string{"provisioning_marker"}, nil }
	})
	r.wifi.connectErr = errors.New("x")
	r.provision()
	if _, out := r.do("POST", "/api/v1/factory-reset", ""); out != `{"success":true,"cleared":["provisioning_marker"],"error":null}` {
		t.Errorf("reset: %s", out)
	}
	if st := r.status(); st != NewStateMachine().Status() {
		t.Errorf("after reset %+v", st)
	}
	r = newRig(t, nil)
	if _, out := r.do("POST", "/api/v1/factory-reset", ""); !strings.HasPrefix(out, `{"success":false,"cleared":[],"error":"`) {
		t.Errorf("no reset func: %s", out)
	}
}

func TestScanAndCaptiveChecks(t *testing.T) {
	r := newRig(t, nil)
	if _, out := r.do("GET", "/api/v1/scan-networks", ""); out != `{"networks":[{"ssid":"HomeNetwork","signal_strength":-45,"security":"WPA2"}]}` {
		t.Errorf("scan %s", out)
	}
	captive := httptest.NewServer(CaptiveHandler())
	t.Cleanup(captive.Close)
	for _, tc := range []struct {
		base, path string
		code       int
		body, ct   string
	}{
		{r.http.URL, "/generate_204", 204, "", ""},
		{r.http.URL, "/hotspot-detect.html", 200, appleSuccess, "text/plain; charset=utf-8"},
		{r.http.URL, "/library/test/success.html", 200, appleSuccess, "text/plain; charset=utf-8"},
		{r.http.URL, "/success.txt", 200, "success", "text/plain; charset=utf-8"},
		{captive.URL, "/generate_204", 204, "", ""},
		{captive.URL, "/hotspot-detect.html?x=1", 200, appleSuccess, "text/html"},
		{captive.URL, "/library/test/success.html", 200, appleSuccess, "text/html"},
		{captive.URL, "/success.txt", 200, "success", "text/plain"},
		{captive.URL, "/connecttest.txt", 204, "", ""},
	} {
		resp, err := http.Get(tc.base + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.code || string(b) != tc.body || (tc.ct != "" && resp.Header.Get("Content-Type") != tc.ct) {
			t.Errorf("%s%s: %d %q %q", tc.base, tc.path, resp.StatusCode, b, resp.Header.Get("Content-Type"))
		}
	}
}

func TestHardware(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, model, want string }{
		{"linux", "arm64", "Raspberry Pi Zero 2 W Rev 1.0\x00", "pi-zero-w"},
		{"linux", "arm", "Raspberry Pi 4 Model B Rev 1.4", "raspberry-pi"},
		{"linux", "arm", "", "arm-linux"},
		{"darwin", "arm64", "", "macos"},
		{"linux", "amd64", "", "linux-x86_64"},
	} {
		if got := hardware(tc.goos, tc.goarch, tc.model); got != tc.want {
			t.Errorf("%+v: %s", tc, got)
		}
	}
}

// A single-port command-center URL (config port + /cc) still gives config's own address.
func TestConfigURLFromAMountedCommandCenter(t *testing.T) {
	if got := configURL(ProvisionRequest{CommandCenterURL: "http://h:7700/cc"}); got != "http://h:7700" {
		t.Fatalf("configURL = %q", got)
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/hat"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/provisioning"
	"github.com/alexberardi/jarvis-server/internal/agent/reset"
	"github.com/alexberardi/jarvis-server/internal/platform/mdns"
)

// setupRadio is a Wi-Fi that records what setup mode does with the radio.
type setupRadio struct {
	provisioning.Simulated
	mu    sync.Mutex
	calls []string
	home  string // the network wlan0 is already on ("": none)
	// connectErr fails every join (a wrong password, a network out of range).
	connectErr error
}

func (r *setupRadio) note(s string) { r.mu.Lock(); r.calls = append(r.calls, s); r.mu.Unlock() }
func (r *setupRadio) Calls() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, ",")
}
func (r *setupRadio) ScanAndCache(context.Context) ([]provisioning.NetworkInfo, error) {
	r.note("scan")
	return nil, nil
}
func (r *setupRadio) StartAPMode(_ context.Context, ssid string) error {
	r.note("ap " + ssid)
	return nil
}
func (r *setupRadio) CurrentSSID(ctx context.Context) (string, error) {
	if r.home != "" {
		return r.home, nil
	}
	return r.Simulated.CurrentSSID(ctx)
}
func (r *setupRadio) Connect(ctx context.Context, ssid, pw string) error {
	r.note("connect " + ssid)
	if r.connectErr != nil {
		return r.connectErr
	}
	return r.Simulated.Connect(ctx, ssid, pw)
}

// ledStrip is a HAT LED strip that keeps nothing.
type ledStrip struct{}

func (ledStrip) Write(b []byte) (int, error) { return len(b), nil }
func (ledStrip) Close() error                { return nil }

// setupRig is setup mode with a recorded radio, an optional fake HAT and the API on a loopback
// port, the address of which it reports once serving.
type setupRig struct {
	radio  *setupRadio
	leds   *hat.LEDs // nil: no HAT
	closed chan struct{}
	ready  chan string

	mu  sync.Mutex
	ads []mdns.Options // what setup mode advertised
	// withdrawn counts the advertisements stopped.
	withdrawn int
}

func (r *setupRig) advertised() ([]mdns.Options, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mdns.Options(nil), r.ads...), r.withdrawn
}

func newSetupRig(t *testing.T, withHAT bool) *setupRig {
	t.Helper()
	r := &setupRig{radio: &setupRadio{}, closed: make(chan struct{}), ready: make(chan string, 1)}
	if withHAT {
		r.leds = hat.NewLEDs(ledStrip{}, slog.New(slog.DiscardHandler))
	}
	old := piSetup
	t.Cleanup(func() { piSetup = old })
	piSetup = piSetupHooks{
		wifi: func() (provisioning.WiFi, error) { return r.radio, nil },
		hat: func(*hatFlags, *slog.Logger) (*hat.LEDs, func()) {
			if r.leds == nil {
				return nil, nil
			}
			return r.leds, func() { r.leds.Close(); close(r.closed) }
		},
		listen:  "127.0.0.1:0",
		captive: "",
		ready:   func(a net.Addr) { r.ready <- a.String() },
		advertise: func(_ context.Context, o mdns.Options) (func(), error) {
			r.mu.Lock()
			r.ads = append(r.ads, o)
			r.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { r.mu.Lock(); r.withdrawn++; r.mu.Unlock() }) }, nil
		},
	}
	return r
}

func (r *setupRig) addr(t *testing.T, errc <-chan error) string {
	t.Helper()
	select {
	case a := <-r.ready:
		return a
	case err := <-errc:
		t.Fatalf("returned before serving setup mode: %v (exit %d)", err, exitCode(err))
	case <-time.After(10 * time.Second):
		t.Fatal("setup mode never served the provisioning API")
	}
	return ""
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil || resp.StatusCode != 200 {
		t.Fatalf("%s: %d %v", url, resp.StatusCode, err)
	}
	return m
}

func agentHome(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	return dir
}

// #292: a Pi with no credentials (a factory reset, or a fresh install not enrolled by code)
// comes up in setup mode: the hotspot after a scan, the provisioning API and, with the HAT, the
// setup LED pattern; it used to exit 78 until systemd gave up, offline for good.
func TestPiRunWithoutCredentialsEntersSetupMode(t *testing.T) {
	for _, withHAT := range []bool{true, false} {
		name := map[bool]string{true: "hat", false: "no hat"}[withHAT]
		t.Run(name, func(t *testing.T) {
			dir := agentHome(t)
			r := newSetupRig(t, withHAT)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			errc := make(chan error, 1)
			go func() {
				errc <- run(ctx, []string{"--profile", "pi", "--creds", filepath.Join(dir, "node.json"),
					"--config", filepath.Join(dir, "agent.json")})
			}()
			a := r.addr(t, errc)
			if info := getJSON(t, "http://"+a+"/api/v1/info"); info["state"] != "AP_MODE" {
				t.Errorf("info %v", info)
			}
			if got := r.radio.Calls(); !strings.HasPrefix(got, "scan,ap jarvis-") {
				t.Errorf("radio %q, want a scan then the jarvis- hotspot", got)
			}
			if withHAT && r.leds.Current() != hat.Setup {
				t.Errorf("LEDs %q, want %q", r.leds.Current(), hat.Setup)
			}
			cancel() // systemd stopping the unit
			select {
			case err := <-errc:
				if err != nil {
					t.Errorf("stopped in setup mode: %v (exit %d), want a clean exit", err, exitCode(err))
				}
			case <-time.After(10 * time.Second):
				t.Fatal("setup mode didn't stop")
			}
		})
	}
}

// #423: an unenrolled Pi already on the home Wi-Fi (installed and activated over SSH on wlan0)
// keeps that network: setup mode serves the provisioning API on its LAN address and starts no
// hotspot, which used to drop the LAN and strand the node.
func TestSetupModeKeepsAWorkingWiFi(t *testing.T) {
	dir := agentHome(t)
	r := newSetupRig(t, true)
	r.radio.home = "Home"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- run(ctx, []string{"--profile", "pi", "--creds", filepath.Join(dir, "node.json"),
			"--config", filepath.Join(dir, "agent.json")})
	}()
	a := r.addr(t, errc)
	if info := getJSON(t, "http://"+a+"/api/v1/info"); info["node_id"] == nil {
		t.Errorf("info %v", info)
	}
	if got := r.radio.Calls(); strings.Contains(got, "ap ") || strings.Contains(got, "scan") {
		t.Errorf("radio %q, want the hotspot left down on a connected wlan0", got)
	}
	if r.leds.Current() != hat.Setup {
		t.Errorf("LEDs %q, want %q", r.leds.Current(), hat.Setup)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("stopped in setup mode: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("setup mode didn't stop")
	}
}

// #423: a failed provisioning attempt on a Pi that kept its Wi-Fi (no hotspot ran) must not start
// one: restoring "the" hotspot would drop the LAN the app reaches the node on, and strand it.
func TestSetupModeOnWiFiFailedAttemptStartsNoHotspot(t *testing.T) {
	dir := agentHome(t)
	path := filepath.Join(dir, "jarvis-agent", "node.json")
	r := newSetupRig(t, false)
	r.radio.home = "Home"
	r.radio.connectErr = errors.New("secrets were required, but not provided")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- piSetupMode(ctx, slog.New(slog.DiscardHandler), path, &hatFlags{}) }()
	a := r.addr(t, errc)
	body, _ := json.Marshal(map[string]any{
		"wifi_ssid": "Elsewhere", "wifi_password": "wrong", "room": "kitchen",
		"command_center_url": "http://192.0.2.1:7703", "household_id": "hh-1",
		"node_id": "cc-node-1", "provisioning_token": "tok-1",
	})
	resp, err := http.Post("http://"+a+"/api/v1/provision", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	deadline := time.Now().Add(10 * time.Second)
	for getJSON(t, "http://"+a+"/api/v1/status")["state"] != string(provisioning.StateError) {
		if time.Now().After(deadline) {
			t.Fatalf("the attempt didn't fail: %v", getJSON(t, "http://"+a+"/api/v1/status"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := r.radio.Calls(); got != "connect Elsewhere" {
		t.Errorf("radio %q, want only the join: no hotspot after a failed attempt on a connected wlan0", got)
	}
	cancel()
	select {
	case <-errc:
	case <-time.After(10 * time.Second):
		t.Fatal("setup mode didn't stop")
	}
}

// After a factory reset (reset.Wipe, then the reboot) the next boot is setup mode; once the app
// provisions it, setup mode ends with the node's credentials saved and the HAT handed back.
func TestResetThenBootSetupModeThenProvisioned(t *testing.T) {
	for _, withHAT := range []bool{true, false} {
		name := map[bool]string{true: "hat", false: "no hat"}[withHAT]
		t.Run(name, func(t *testing.T) {
			dir := agentHome(t)
			path := filepath.Join(dir, "jarvis-agent", "node.json")
			if err := node.SaveCreds(path, node.Creds{ConfigURL: "http://192.0.2.1:7700", NodeID: "old", NodeKey: "k"}); err != nil {
				t.Fatal(err)
			}
			if _, err := reset.Wipe(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			if _, err := node.LoadCreds(path); err == nil {
				t.Fatal("the reset kept the credentials")
			}
			cc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"node_id":"cc-node-1","room":"kitchen","node_key":"secret-key"}`)
			}))
			defer cc.Close()
			r := newSetupRig(t, withHAT)
			errc := make(chan error, 1)
			go func() { errc <- piSetupMode(t.Context(), slog.New(slog.DiscardHandler), path, &hatFlags{}) }()
			a := r.addr(t, errc)
			if ads, _ := r.advertised(); len(ads) != 0 {
				t.Errorf("advertised %+v on the hotspot, want only the LAN setup advertised", ads)
			}
			body, _ := json.Marshal(map[string]any{
				"wifi_ssid": "HomeNetwork", "wifi_password": "hunter22", "room": "kitchen",
				"command_center_url": cc.URL, "config_service_url": "http://192.0.2.10:7700",
				"household_id": "hh-1", "node_id": "cc-node-1", "provisioning_token": "tok-1",
			})
			resp, err := http.Post("http://"+a+"/api/v1/provision", "application/json", strings.NewReader(string(body)))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			select {
			case err := <-errc:
				if err != nil {
					t.Fatalf("setup mode: %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("setup mode didn't end once provisioned")
			}
			c, err := node.LoadCreds(path)
			if err != nil || c.NodeID != "cc-node-1" || c.NodeKey != "secret-key" {
				t.Errorf("creds %+v, %v", c, err)
			}
			if !strings.Contains(r.radio.Calls(), "connect HomeNetwork") {
				t.Errorf("radio %q", r.radio.Calls())
			}
			if withHAT {
				select {
				case <-r.closed: // the node's own run opens it again
				default:
					t.Error("the HAT was not handed back after setup mode")
				}
			}
		})
	}
}

// #508: a Pi in setup mode on its Wi-Fi (after a factory reset) advertises _jarvis-setup._tcp
// with its node id, hardware and the API's port, so the app's Add node flow finds it without the
// hotspot; the advertisement is withdrawn once the node is provisioned.
func TestSetupModeOnWiFiAdvertisesUntilProvisioned(t *testing.T) {
	dir := agentHome(t)
	path := filepath.Join(dir, "jarvis-agent", "node.json")
	cc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"node_id":"cc-node-1","room":"kitchen","node_key":"secret-key"}`)
	}))
	defer cc.Close()
	r := newSetupRig(t, false)
	r.radio.home = "Home"
	errc := make(chan error, 1)
	go func() { errc <- piSetupMode(t.Context(), slog.New(slog.DiscardHandler), path, &hatFlags{}) }()
	a := r.addr(t, errc)
	_, port, _ := net.SplitHostPort(a)
	info := getJSON(t, "http://"+a+"/api/v1/info")
	ads, withdrawn := r.advertised()
	if len(ads) != 1 || withdrawn != 0 {
		t.Fatalf("advertised %+v (withdrawn %d), want one live advertisement", ads, withdrawn)
	}
	ad := ads[0]
	if ad.Service != "_jarvis-setup._tcp" || strconv.Itoa(ad.Port) != port || ad.Instance != info["node_id"] ||
		ad.TXT["node_id"] != info["node_id"] || ad.TXT["hardware"] != info["hardware"] {
		t.Errorf("advertised %+v, want _jarvis-setup._tcp on port %s for %v", ad, port, info)
	}
	body, _ := json.Marshal(map[string]any{
		"wifi_ssid": "HomeNetwork", "wifi_password": "hunter22", "room": "kitchen",
		"command_center_url": cc.URL, "config_service_url": "http://192.0.2.10:7700",
		"household_id": "hh-1", "node_id": "cc-node-1", "provisioning_token": "tok-1",
	})
	resp, err := http.Post("http://"+a+"/api/v1/provision", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("setup mode: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("setup mode didn't end once provisioned")
	}
	if ads, withdrawn := r.advertised(); len(ads) != 1 || withdrawn != 1 {
		t.Errorf("advertised %d, withdrawn %d after provisioning; want the one advertisement withdrawn", len(ads), withdrawn)
	}
}

// A Pi's setup pattern is its own: nothing else on the HAT looks like it.
func TestSetupLEDPatternIsDistinct(t *testing.T) {
	if !hat.Known(hat.Setup) {
		t.Fatal("no setup pattern")
	}
	for _, other := range []string{hat.Normal, hat.Alert, hat.WakeDetected, hat.Listening, hat.Thinking,
		hat.Speaking, hat.Error, hat.NotForMe, hat.Muted, hat.ShutdownWarning, hat.Off} {
		for _, at := range []float64{0, 0.5, 1, 1.5} {
			s, o := hat.Render(hat.Setup, at, 100), hat.Render(other, at, 100)
			if s[0].R == o[0].R && s[0].G == o[0].G && s[0].B == o[0].B {
				t.Errorf("setup looks like %s at %.1fs", other, at)
			}
		}
	}
}

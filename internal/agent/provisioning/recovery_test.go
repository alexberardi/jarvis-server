package provisioning

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// cycleWiFi is fakeWiFi whose CurrentSSID answers from joined (one per call; "" once used up).
type cycleWiFi struct {
	fakeWiFi
	joined  []string
	stopErr error
}

func (c *cycleWiFi) StopAPMode(ctx context.Context) error {
	c.note("ap down")
	return c.stopErr
}

func (c *cycleWiFi) CurrentSSID(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "ssid?")
	if len(c.joined) == 0 {
		return "", nil
	}
	s := c.joined[0]
	c.joined = c.joined[1:]
	return s, nil
}

// recoveryRig runs r with a fake clock: each sleep is noted in calls as "wait <d>", and ctx
// ends after maxSleeps sleeps.
func recoveryRig(t *testing.T, w *cycleWiFi, pairing func() bool, maxSleeps int) (rebooted *int) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rebooted = new(int)
	sleeps := 0
	r := Recovery{
		WiFi: w, APSSID: "jarvis-ab12", Pairing: pairing,
		Reboot:        func() error { *rebooted++; w.note("reboot"); return nil },
		RetryInterval: 600 * time.Second, STAWindow: 15 * time.Second, Poll: 5 * time.Second,
		sleep: func(ctx context.Context, d time.Duration) bool {
			w.note(fmt.Sprintf("wait %s", d))
			if sleeps++; sleeps >= maxSleeps {
				cancel()
				return false
			}
			return ctx.Err() == nil
		},
	}
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	return rebooted
}

func TestRecoveryJoinedReboots(t *testing.T) {
	w := &cycleWiFi{joined: []string{"", "Home"}}
	rebooted := recoveryRig(t, w, func() bool { return false }, 100)
	if *rebooted != 1 {
		t.Errorf("rebooted %d times", *rebooted)
	}
	want := "wait 10m0s,ap down,ssid?,wait 5s,ssid?,reboot"
	if got := w.Calls(); got != want {
		t.Errorf("calls\n got %s\nwant %s", got, want)
	}
}

func TestRecoveryNotJoinedRestoresTheHotspot(t *testing.T) {
	w := &cycleWiFi{}
	rebooted := recoveryRig(t, w, func() bool { return false }, 5)
	if *rebooted != 0 {
		t.Error("rebooted without a network")
	}
	// The STA window is 15 s at 5 s polls: three looks, then the hotspot again, then the next
	// AP phase.
	want := "wait 10m0s,ap down,ssid?,wait 5s,ssid?,wait 5s,ssid?,wait 5s,ap jarvis-ab12,wait 10m0s"
	if got := w.Calls(); got != want {
		t.Errorf("calls\n got %s\nwant %s", got, want)
	}
}

func TestRecoveryKeepsTheHotspotWhilePairing(t *testing.T) {
	w := &cycleWiFi{joined: []string{"Home"}}
	recoveryRig(t, w, func() bool { return true }, 3)
	if got := w.Calls(); got != "wait 10m0s,wait 10m0s,wait 10m0s" {
		t.Errorf("calls %s", got)
	}
}

func TestRecoveryStopFailureKeepsCycling(t *testing.T) {
	w := &cycleWiFi{stopErr: errors.New("no bus")}
	recoveryRig(t, w, func() bool { return false }, 2)
	if got := w.Calls(); got != "wait 10m0s,ap down,wait 10m0s" {
		t.Errorf("calls %s", got)
	}
}

func TestRecoveryFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	r, ok := RecoveryFromEnv(env(nil))
	if !ok || r.RetryInterval != 600*time.Second || r.STAWindow != 120*time.Second || r.Poll != 5*time.Second {
		t.Errorf("defaults: %v %+v", ok, r)
	}
	r, ok = RecoveryFromEnv(env(map[string]string{
		"JARVIS_RECOVERY_RETRY_INTERVAL": "60", "JARVIS_RECOVERY_STA_WINDOW": "30.5", "JARVIS_RECOVERY_POLL_SECONDS": "2",
	}))
	if !ok || r.RetryInterval != 60*time.Second || r.STAWindow != 30500*time.Millisecond || r.Poll != 2*time.Second {
		t.Errorf("set: %+v", r)
	}
	// Python's floors: 30 s, 15 s, 1 s. A value that isn't a number is the default.
	r, _ = RecoveryFromEnv(env(map[string]string{
		"JARVIS_RECOVERY_RETRY_INTERVAL": "1", "JARVIS_RECOVERY_STA_WINDOW": "1", "JARVIS_RECOVERY_POLL_SECONDS": "x",
	}))
	if r.RetryInterval != 30*time.Second || r.STAWindow != 15*time.Second || r.Poll != 5*time.Second {
		t.Errorf("floors: %+v", r)
	}
	if _, ok := RecoveryFromEnv(env(map[string]string{"JARVIS_RECOVERY_DISABLED": "1"})); ok {
		t.Error("JARVIS_RECOVERY_DISABLED ignored")
	}
}

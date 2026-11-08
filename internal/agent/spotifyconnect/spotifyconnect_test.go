package spotifyconnect

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteConfigListensOnLoopbackAndQuotesTheName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spotify")
	if err := WriteConfig(dir, `Kitchen "Pi": 1`, Port); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{`device_name: "Kitchen \"Pi\": 1"`, "address: 127.0.0.1", "port: 3678", "persist_credentials: true", "audio_backend: pulseaudio"} {
		if !strings.Contains(got, want) {
			t.Errorf("config lacks %q:\n%s", want, got)
		}
	}
	if info, _ := os.Stat(dir); runtime.GOOS != "windows" && info.Mode().Perm() != 0700 { // Windows has no Unix modes
		t.Errorf("config dir mode %v, want 0700", info.Mode().Perm())
	}
}

func TestRunRestartsAnExitedReceiverAndStopsWithTheContext(t *testing.T) {
	var starts atomic.Int32
	r := &Receiver{Dir: t.TempDir(), Name: "Test", Backoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond,
		Command: func(ctx context.Context) *exec.Cmd {
			starts.Add(1)
			return exec.CommandContext(ctx, "true")
		}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want the context's error", err)
	}
	if n := starts.Load(); n < 3 {
		t.Errorf("receiver started %d times, want it restarted after each exit", n)
	}
}

func TestRunReportsAMissingBinaryAtOnce(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	done := make(chan error, 1)
	go func() { done <- (&Receiver{Dir: t.TempDir(), Name: "Test"}).Run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("Run = %v, want ErrNotInstalled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run kept going without go-librespot installed")
	}
}

func TestLanInterfacesSkipsContainersVPNsAndInterfacesWithNoIPv4(t *testing.T) {
	up := net.FlagUp | net.FlagMulticast
	got := lanInterfaces([]iface{
		{Name: "lo", Flags: up | net.FlagLoopback, HasIPv4: true},
		{Name: "wlp1s0", Flags: up | net.FlagBroadcast, HasIPv4: true},
		{Name: "eth0", Flags: up, HasIPv4: true},
		{Name: "docker0", Flags: up, HasIPv4: true},
		{Name: "br-1313ba7f575d", Flags: up, HasIPv4: true},
		{Name: "tailscale0", Flags: up | net.FlagPointToPoint, HasIPv4: true},
		{Name: "veth12ab", Flags: up, HasIPv4: true},
		{Name: "wlan1", Flags: up, HasIPv4: false},              // no address yet
		{Name: "eth1", Flags: net.FlagMulticast, HasIPv4: true}, // down
		{Name: "eth2", Flags: net.FlagUp, HasIPv4: true},        // no multicast
	})
	if len(got) != 2 || got[0] != "wlp1s0" || got[1] != "eth0" {
		t.Fatalf("lanInterfaces = %v, want [wlp1s0 eth0]", got)
	}
}

func TestWriteConfigAnnouncesOnLanInterfacesOnly(t *testing.T) {
	defer func(f func() []iface) { interfaces = f }(interfaces)
	interfaces = func() []iface {
		return []iface{{Name: "wlp1s0", Flags: net.FlagUp | net.FlagMulticast, HasIPv4: true}, {Name: "docker0", Flags: net.FlagUp | net.FlagMulticast, HasIPv4: true}}
	}
	dir := t.TempDir()
	if err := WriteConfig(dir, "Test", Port); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
	if !strings.Contains(string(raw), `zeroconf_interfaces_to_advertise: ["wlp1s0"]`) {
		t.Errorf("config:\n%s", raw)
	}
	// No usable interface: leave the option out, so go-librespot keeps its default.
	interfaces = func() []iface { return nil }
	if err := WriteConfig(dir, "Test", Port); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "config.yml"))
	if strings.Contains(string(raw), "zeroconf_interfaces_to_advertise") {
		t.Errorf("option written with no interfaces:\n%s", raw)
	}
}

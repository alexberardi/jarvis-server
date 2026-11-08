package spotifyconnect

import (
	"context"
	"errors"
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

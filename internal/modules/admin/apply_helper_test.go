package admin

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/service"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// TestApplyViaHelper: the binary's directory isn't writable (a LaunchDaemon or Windows service
// install), but an on-demand privileged helper is installed (ID11): the button is offered,
// stages, wakes the helper, and leaves the swap and the restart to it. A refusal by the helper
// fails the job with its reason.
func TestApplyViaHelper(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this process can't write (unix, not root)")
	}
	noRestartDelay(t)
	t.Setenv(EnvAllowUpdates, "1")
	k := newReleaseKey(t)
	srv := signedRelease(t, k, "v1.1.0", []byte("new binary"))
	for _, refuse := range []bool{false, true} {
		e := newA4(t, "v1.0.0")
		bin := t.TempDir()
		exe := filepath.Join(bin, "jarvisd")
		if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(bin, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(bin, 0o700) })
		r := &fakeRestarter{kind: service.Launchd}
		e.m.Restarter = r
		e.m.Updates = UpdateOptions{APIBase: srv.URL}
		e.m.Upgrade = UpgradeConfig{Exe: exe, Keys: []update.PublicKey{k.pub}, SkipVersionCheck: true}
		// No helper: the button is blocked with the command.
		if st := decode(t, send(e.mux, "GET", "/api/update", "", root...)); st["can_apply"] != false || st["apply_command"] != "sudo jarvisd upgrade" {
			t.Fatalf("no helper: %v", st)
		}
		var triggers atomic.Int32
		p := update.Paths{Home: e.m.deps.Config.Home, Exe: exe}
		e.m.Upgrade.Helper = service.HelperLaunchd
		e.m.Upgrade.Trigger = func(context.Context) error {
			triggers.Add(1)
			mk, err := update.ReadMarker(p)
			if err != nil || mk == nil || mk.State != update.StateStaged {
				return err
			}
			if refuse {
				return update.Abort(p, "update: signature is INVALID")
			}
			mk.State = update.StateSwapped // what the helper writes after the swap
			return update.WriteMarker(p, mk)
		}
		st := decode(t, send(e.mux, "GET", "/api/update", "", root...))
		info := decode(t, send(e.mux, "GET", "/api/system/info", "", root...))
		if caps, _ := info["capabilities"].(map[string]any); st["can_apply"] != true || caps["self_update"] != true {
			t.Fatalf("helper: %v %v", st, info["capabilities"])
		}
		if w := send(e.mux, "POST", "/api/update/apply", "", root...); w.Code != http.StatusAccepted {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		want := "restarting"
		if refuse {
			want = "failed"
		}
		var job map[string]any
		waitFor(t, "the helper", func() bool {
			job, _ = decode(t, send(e.mux, "GET", "/api/update/apply", "", root...))["job"].(map[string]any)
			return triggers.Load() > 0 && job["state"] == want
		})
		if refuse && (!strings.Contains(fmt.Sprint(job["error"]), "refused") || !strings.Contains(fmt.Sprint(job["error"]), "INVALID")) {
			t.Fatalf("job %v", job)
		}
		// The helper restarts jarvisd, not the admin.
		time.Sleep(50 * time.Millisecond)
		if r.requested.Load() != 0 {
			t.Fatal("the admin restarted jarvisd itself")
		}
		if b, _ := os.ReadFile(exe); string(b) != "old binary" {
			t.Fatal("the admin swapped without write access?")
		}
	}
}

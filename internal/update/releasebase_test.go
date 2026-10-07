package update

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A flat release directory (install.sh --base-url / JARVISD_RELEASE_BASE) stands in for GitHub:
// the release is the one its SHA256SUMS names, and the signature is still required.
func TestStageFromReleaseBase(t *testing.T) {
	gh, in, o, bin := setup(t)
	ctx := context.Background()
	o.Source = Source{ReleaseBase: gh.srv.URL + "/download/v1.1.0/", UserAgent: "test"}

	_, err := Resolve(ctx, StageOptions{Current: "v1.0.0", Target: "v0.9.0", AllowOlder: true, Source: o.Source})
	if !errors.Is(err, ErrNoRelease) || !strings.Contains(err.Error(), "holds v1.1.0") {
		t.Fatalf("other tag: %v", err)
	}
	plan, err := Resolve(ctx, StageOptions{Current: "v1.0.0", Source: o.Source})
	if err != nil || plan.Release.Tag != "v1.1.0" {
		t.Fatalf("newest: %+v %v", plan, err)
	}
	o.Target = "v1.1.0"
	if _, err := Stage(ctx, o); err != nil {
		t.Fatal(err)
	}
	if _, err := Swap(ctx, in.paths, SwapOptions{Keys: o.Keys}); err != nil {
		t.Fatal(err)
	}
	if readString(t, in.paths.Exe) != string(bin) {
		t.Fatal("swap didn't install the flat release's binary")
	}

	// Unsigned directory: refused before the archive is downloaded.
	s := newTestSigner(t)
	gh.add(releaseSpec{tag: "v1.3.0", platform: Platform(), binary: bin, signer: &s, noSig: true})
	_, err = Resolve(ctx, StageOptions{Current: "v1.1.0", Source: Source{ReleaseBase: gh.srv.URL + "/download/v1.3.0"}})
	if err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("unsigned: %v", err)
	}
	// Nothing there.
	if _, err := Resolve(ctx, StageOptions{Current: "v1.1.0", Source: Source{ReleaseBase: gh.srv.URL + "/nope"}}); err == nil {
		t.Fatal("missing directory accepted")
	}
}

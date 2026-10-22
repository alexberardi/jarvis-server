//go:build release_xpi

package browser

import "testing"

// The release gate (release.yml runs it before building): the agent must ship the Mozilla-signed
// package of the extension source it carries, or Firefox users run an old extension while the
// agent's tools expect the new one (#449: 0.1.0 shipped beside 0.3.4). Re-sign with
// scripts/browser-ext-sign.sh (docs/agent/store-listing.md) and commit the .xpi.
//
//	go test ./internal/agent/browser -run TestSignedXPICurrent -tags release_xpi
func TestSignedXPICurrent(t *testing.T) {
	if _, ok := SignedXPI(); !ok {
		t.Fatal("this build has no signed Firefox package (internal/agent/browser/signed/jarvis-browser.xpi)")
	}
	if s, src := SignedXPIVersion(), ExtensionVersion(); CompareVersions(s, src) != 0 {
		t.Fatalf("the signed Firefox package is %s but the extension source is %s: run scripts/browser-ext-sign.sh and commit the .xpi", s, src)
	}
}

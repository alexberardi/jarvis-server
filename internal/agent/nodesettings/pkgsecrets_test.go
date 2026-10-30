package nodesettings

import (
	"context"
	"testing"
)

func TestPackageSecretsPullStoresChangesAndClears(t *testing.T) {
	h, f, _ := newHandler(t, "n1")
	changed := 0
	h.SecretsChanged = func() { changed++ }
	ctx := context.Background()

	// A nudge pulls what jarvisd holds and stores it, with no K2 on the node.
	f.pkgSecrets = map[string]string{"HA_URL": "http://ha:8123", "HA_TOKEN": "tok"}
	if !h.Handle(ctx, TopicPackageSecrets, []byte(`{}`)) {
		t.Fatal("the topic is not handled")
	}
	if h.Store.Secret("HA_URL", "integration", nil) != "http://ha:8123" || h.Store.Secret("HA_TOKEN", "integration", nil) != "tok" {
		t.Fatal("secrets not stored")
	}
	if h.Store.HasK2() || changed != 1 {
		t.Fatalf("k2 %v, changed %d", h.Store.HasK2(), changed)
	}

	// The same pull again changes nothing and does not announce a change.
	h.PackageSecrets(ctx)
	if changed != 1 {
		t.Fatalf("an unchanged pull announced a change (%d)", changed)
	}

	// A new value replaces the old; "" (deleted on jarvisd) clears it.
	f.pkgSecrets = map[string]string{"HA_URL": "http://ha2:8123", "HA_TOKEN": ""}
	h.PackageSecrets(ctx)
	if h.Store.Secret("HA_URL", "integration", nil) != "http://ha2:8123" || h.Store.Secret("HA_TOKEN", "integration", nil) != "" {
		t.Fatal("update or clear not applied")
	}
	if changed != 2 {
		t.Fatal(changed)
	}

	// A key the node already keeps under another scope keeps that scope; a user's own secret is left alone.
	if err := h.Store.SetSecret("KEEP_TOKEN", "mine", "user", "", ptr(int64(7))); err != nil {
		t.Fatal(err)
	}
	f.pkgSecrets = map[string]string{"KEEP_TOKEN": "household"}
	h.PackageSecrets(ctx)
	if h.Store.Secret("KEEP_TOKEN", "user", ptr(int64(7))) != "mine" {
		t.Fatal("overwrote a user's secret")
	}

	// An older jarvisd without the route is not an error worth stopping for.
	f.pkgSecrets = nil
	h.PackageSecrets(ctx)
}

func TestBackstopPullsPackageSecrets(t *testing.T) {
	h, f, _ := newHandler(t, "n1")
	f.pkgSecrets = map[string]string{"HA_URL": "http://ha:8123"}
	h.Backstop(context.Background())
	if h.Store.Secret("HA_URL", "integration", nil) != "http://ha:8123" {
		t.Fatal("the reconnect backstop did not pull package secrets")
	}
}

func ptr[T any](v T) *T { return &v }

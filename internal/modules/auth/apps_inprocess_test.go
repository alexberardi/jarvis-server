package auth

import (
	"context"
	"errors"
	"testing"
)

func TestAppClientsInProcess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	valid := func(id, key string) bool {
		t.Helper()
		_, ok, err := e.m.ValidateApp(ctx, id, key)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	a, key, err := e.m.CreateAppClient(ctx, "jarvis-web", "Web chat")
	if err != nil || !a.IsActive || a.CreatedAt == "" || a.LastRotatedAt != nil || len(key) < 40 || !valid("jarvis-web", key) {
		t.Fatalf("create: %+v %q %v", a, key, err)
	}
	if _, _, err := e.m.CreateAppClient(ctx, "jarvis-web", "again"); !errors.Is(err, ErrAppExists) {
		t.Fatalf("duplicate: %v", err)
	}

	newKey, rotated, err := e.m.RotateAppClient(ctx, "jarvis-web")
	if err != nil || newKey == key || rotated == "" {
		t.Fatalf("rotate: %v", err)
	}
	if valid("jarvis-web", key) || !valid("jarvis-web", newKey) {
		t.Fatal("rotation did not replace the key")
	}

	if err := e.m.RevokeAppClient(ctx, "jarvis-web"); err != nil || valid("jarvis-web", newKey) {
		t.Fatalf("revoke: %v", err)
	}
	for _, err := range []error{
		e.m.RevokeAppClient(ctx, "nope"),
		func() error { _, _, err := e.m.RotateAppClient(ctx, "nope"); return err }(),
	} {
		if !errors.Is(err, ErrAppNotFound) {
			t.Fatalf("unknown app: %v", err)
		}
	}

	apps, err := e.m.AppClients(ctx)
	if err != nil || len(apps) != 2 || apps[0].AppID != "test-app" || apps[1].AppID != "jarvis-web" ||
		apps[1].IsActive || apps[1].LastRotatedAt == nil {
		t.Fatalf("list: %+v %v", apps, err)
	}
}

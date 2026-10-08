package auth

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestSelfAppCreds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	id, key, err := e.m.SelfAppCreds(ctx)
	if err != nil || id != SelfAppID || key == "" {
		t.Fatalf("first call: %q %q %v", id, key, err)
	}
	if _, ok, _ := e.m.ValidateApp(ctx, id, key); !ok {
		t.Fatal("the issued key does not validate")
	}
	// /internal/app-ping is what a callback receiver asks.
	if code, body := e.do("GET", "/internal/app-ping", nil, hdrs{"X-Jarvis-App-Id": id, "X-Jarvis-App-Key": key}); code != 200 || body["app_id"] != SelfAppID {
		t.Fatalf("app-ping: %d %v", code, body)
	}
	path := SelfAppKeyPath(e.home)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", st, err)
	}
	b, _ := os.ReadFile(path)
	if strings.TrimSpace(string(b)) != key {
		t.Fatal("key file does not hold the key")
	}

	// A restart (new process, same home) reuses the file instead of rotating.
	e.m.ForgetSelfAppKey()
	if _, again, err := e.m.SelfAppCreds(ctx); err != nil || again != key {
		t.Fatalf("restart: %q %v (want the same key)", again, err)
	}

	// Revoked from the Connections page: the next use reissues (and reactivates) it.
	if err := e.m.RevokeAppClient(ctx, SelfAppID); err != nil {
		t.Fatal(err)
	}
	_, fresh, err := e.m.SelfAppCreds(ctx)
	if err != nil || fresh == key {
		t.Fatalf("after revoke: %q %v", fresh, err)
	}
	if _, ok, _ := e.m.ValidateApp(ctx, SelfAppID, fresh); !ok {
		t.Fatal("the reissued key does not validate")
	}
	if _, ok, _ := e.m.ValidateApp(ctx, SelfAppID, key); ok {
		t.Fatal("the old key still validates")
	}

	// A deleted key file with the row still there: rotated, not a duplicate-row error.
	os.Remove(path)
	e.m.ForgetSelfAppKey()
	if _, k, err := e.m.SelfAppCreds(ctx); err != nil || k == fresh {
		t.Fatalf("lost key file: %q %v", k, err)
	}
}

package authn

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestBearerToken(t *testing.T) {
	for h, want := range map[string]string{
		"Bearer abc":   "abc",
		"bearer  abc ": "abc",
		"Basic abc":    "",
		"Bearer":       "",
		"":             "",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if h != "" {
			r.Header.Set("Authorization", h)
		}
		if got := BearerToken(r); got != want {
			t.Errorf("%q: got %q want %q", h, got, want)
		}
	}
}

func TestNodeKey(t *testing.T) {
	if id, k, ok := NodeKey("node-1:s3cr:et"); !ok || id != "node-1" || k != "s3cr:et" {
		t.Fatalf("got %q %q %v", id, k, ok)
	}
	for _, bad := range []string{"barekey", ":key", "node:", ""} {
		if _, _, ok := NodeKey(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEqual(t *testing.T) {
	if !Equal("x", "x") || Equal("x", "y") || Equal("", "") {
		t.Fatal("Equal wrong")
	}
}

func TestContextPrincipals(t *testing.T) {
	ctx := WithNode(WithUser(context.Background(), User{ID: 7}), Node{ID: "n"})
	if u, ok := UserFrom(ctx); !ok || u.ID != 7 {
		t.Fatal("user")
	}
	if n, ok := NodeFrom(ctx); !ok || n.ID != "n" {
		t.Fatal("node")
	}
	if _, ok := AppFrom(ctx); ok {
		t.Fatal("app should be absent")
	}
}

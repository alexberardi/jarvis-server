package auth

import (
	"context"
	"testing"
)

func TestNodeNamesInProcess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.register()
	for id, name := range map[string]string{"desk": "omarchy", "pi": "pi"} {
		if _, err := e.m.RegisterNode(ctx, id, u.household, name, []string{"jarvis-command-center"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.m.NodeNames(ctx, []string{"desk", "pi", "unknown"})
	if err != nil || len(got) != 2 || got["desk"] != "omarchy" || got["pi"] != "pi" {
		t.Fatalf("names: %v %v", got, err)
	}
	if got, err := e.m.NodeNames(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("no ids: %v %v", got, err)
	}
}

// A node's heartbeat renames its own registration: only that active row changes, and the
// same name writes nothing.
func TestRenameNodeInProcess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.register()
	for id, name := range map[string]string{"desk": "omarchy", "pi": "pi", "old": "old"} {
		if _, err := e.m.RegisterNode(ctx, id, u.household, name, []string{"jarvis-command-center"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.m.DeactivateNode(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, name string }{{"desk", "studio"}, {"desk", "studio"}, {"old", "renamed"}, {"unknown", "x"}} {
		if err := e.m.RenameNode(ctx, c.id, c.name); err != nil {
			t.Fatalf("rename %s: %v", c.id, err)
		}
	}
	got, err := e.m.NodeNames(ctx, []string{"desk", "pi", "old", "unknown"})
	if err != nil || len(got) != 3 || got["desk"] != "studio" || got["pi"] != "pi" || got["old"] != "old" {
		t.Fatalf("names: %v %v", got, err)
	}
}

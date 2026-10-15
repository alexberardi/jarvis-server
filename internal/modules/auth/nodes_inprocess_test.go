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

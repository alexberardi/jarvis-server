package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunCommands(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	ctx := context.Background()

	var out bytes.Buffer
	if err := run(ctx, []string{"version"}, &out); err != nil || strings.TrimSpace(out.String()) != version {
		t.Fatalf("version: %v %q", err, out.String())
	}

	out.Reset()
	if err := run(ctx, []string{"migrate", "status"}, &out); err != nil || !strings.Contains(out.String(), "MODULE") {
		t.Fatalf("migrate status: %v %q", err, out.String())
	}

	for _, bad := range [][]string{nil, {"bogus"}, {"migrate"}} {
		if err := run(ctx, bad, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: want error", bad)
		}
	}
}

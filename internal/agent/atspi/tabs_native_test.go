package atspi

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// JARVIS_ATSPI_TABS_PID=<pid> lists that app's tabs on the real accessibility bus (read-only).
func TestNativeTabsReadOnly(t *testing.T) {
	pid, _ := strconv.Atoi(os.Getenv("JARVIS_ATSPI_TABS_PID"))
	if pid == 0 {
		t.Skip("JARVIS_ATSPI_TABS_PID not set")
	}
	tabs, err := Tabs(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tabs {
		t.Logf("%d %q selected=%v", tb.Index, tb.Name, tb.Selected)
	}
}

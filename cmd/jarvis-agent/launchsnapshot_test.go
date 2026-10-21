package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/launches"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
)

// macOS's launch snapshot keeps apps' own windows, by window number and pid.
func TestWindowOwnersKeepsAppWindows(t *testing.T) {
	list := func() ([]screen.MacWindow, error) {
		return []screen.MacWindow{
			{ID: 7, PID: 300, Layer: 0, W: 800, H: 600},
			{ID: 8, PID: 301, Layer: 25, W: 30, H: 22}, // a status item
			{ID: 9, PID: 302, Layer: 0, W: 1, H: 1},    // a helper
			{ID: 10, PID: 0, Layer: 0, W: 800, H: 600}, // no owner
			{ID: 11, PID: 300, Layer: 0, W: 400, H: 300},
		}, nil
	}
	got, err := windowOwners(list)(context.Background())
	want := []launches.Owned{{ID: "7", Owner: "300"}, {ID: "11", Owner: "300"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := windowOwners(func() ([]screen.MacWindow, error) { return nil, errors.New("no") })(context.Background()); err == nil {
		t.Fatal("error lost")
	}
}

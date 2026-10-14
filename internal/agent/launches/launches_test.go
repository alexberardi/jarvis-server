package launches

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRecordAndRecent(t *testing.T) {
	now := time.Unix(1000, 0)
	tr := &Tracker{Now: func() time.Time { return now }, Windows: func(context.Context) ([]string, error) { return []string{"a", "b"}, nil }}
	if _, ok := tr.Recent(time.Minute); ok {
		t.Fatal("nothing launched yet")
	}
	tr.Record(context.Background(), "Neovim", "nvim")
	l, ok := tr.Recent(time.Minute)
	if !ok || l.Name != "Neovim" || l.ID != "nvim" || !l.Before["a"] || !l.Before["b"] || len(l.Before) != 2 {
		t.Fatalf("%+v %v", l, ok)
	}
	now = now.Add(61 * time.Second)
	if _, ok := tr.Recent(time.Minute); ok {
		t.Fatal("a launch a minute ago is not recent")
	}
}

func TestUnlistableWindowsLeaveBeforeNil(t *testing.T) {
	tr := &Tracker{Windows: func(context.Context) ([]string, error) { return nil, errors.New("no session") }}
	tr.Record(context.Background(), "Steam", "")
	if l, ok := tr.Recent(time.Minute); !ok || l.Before != nil {
		t.Fatalf("%+v", l)
	}
	var nilT *Tracker
	nilT.Record(context.Background(), "x", "")
	if _, ok := nilT.Recent(time.Minute); ok {
		t.Fatal("nil tracker")
	}
}

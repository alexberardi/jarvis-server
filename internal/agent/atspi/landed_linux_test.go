package atspi

import (
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// note keeps what gained the focus and a table's new active cell, per app, and nothing else.
func TestLandingsNote(t *testing.T) {
	l := &Landings{last: map[string]landing{}, pids: map[string]int{}, wake: make(chan struct{})}
	at := time.Unix(100, 0)
	sig := func(member string, path dbus.ObjectPath, body ...any) *dbus.Signal {
		return &dbus.Signal{Sender: ":1.7", Path: path, Name: ifEventObject + "." + member, Body: body}
	}
	any0 := dbus.MakeVariant(int32(0))
	l.note(sig("StateChanged", "/a/lost", "focused", int32(0), int32(0), any0), at)
	l.note(sig("StateChanged", "/a/shown", "showing", int32(1), int32(0), any0), at)
	l.note(sig("TextChanged", "/a/text", "insert", int32(1), int32(0), any0), at)
	if len(l.last) != 0 {
		t.Fatalf("kept %+v", l.last)
	}
	wake := l.wake
	l.note(sig("StateChanged", "/a/entry", "focused", int32(1), int32(0), any0), at)
	if got := l.last[":1.7"]; got.focus.Path != "/a/entry" || !got.focusAt.Equal(at) {
		t.Fatalf("focused: %+v", got)
	}
	select {
	case <-wake:
	default:
		t.Fatal("a landing woke nobody")
	}
	cell := dbus.MakeVariant(objRef{Name: ":1.7", Path: "/a/table/cell"})
	l.note(sig("ActiveDescendantChanged", "/a/table", "", int32(5), int32(0), cell), at.Add(time.Second))
	// The sheet gaining the focus after its cell became the active one doesn't hide the cell.
	l.note(sig("StateChanged", "/a/table", "focused", int32(1), int32(0), any0), at.Add(time.Second))
	if got := l.last[":1.7"]; got.active.Path != "/a/table/cell" || got.focus.Path != "/a/table" {
		t.Fatalf("active descendant: %+v", got)
	}
	l.pids[":1.7"] = 42
	if ref, ok := l.find(t.Context(), 42, at); !ok || ref.Path != "/a/table/cell" {
		t.Fatalf("find = %+v, %v, want the cell over the sheet", ref, ok)
	}
	if _, ok := l.find(t.Context(), 42, at.Add(2*time.Second)); ok {
		t.Fatal("found a landing from before the click")
	}
	if ref, ok := l.find(t.Context(), 42, at.Add(time.Second)); !ok || ref.Path != "/a/table/cell" {
		t.Fatalf("find = %+v, %v", ref, ok)
	}
	if _, ok := l.find(t.Context(), 43, at); ok {
		t.Fatal("found another app's landing")
	}
}

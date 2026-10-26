package views

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
)

type wins []computer.DesktopWindow

func (w wins) List(context.Context) ([]computer.DesktopWindow, error) { return w, nil }

func TestSourceReadsTheRightWindowsTabs(t *testing.T) {
	var selected []string
	byPID := map[int][]atspi.Tab{
		10: {{Index: 0, Name: "Claude"}, {Index: 1, Name: "YouTube", Selected: true}},
		20: {{Index: 0, Name: "Budget"}, {Index: 1, Name: "Summary"}},
	}
	s := &Source{
		Windows: wins{
			{App: "foot", PID: 30, Title: "~"},
			{App: "firefox", PID: 10, Title: "YouTube — Mozilla Firefox"},
			{App: "libreoffice-calc", PID: 20, Title: "budget.ods", Focused: true},
		},
		Tabs: func(_ context.Context, pid int) ([]atspi.Tab, error) {
			if pid == 30 {
				return nil, errors.New("no accessible app with pid 30")
			}
			return byPID[pid], nil
		},
		Select: func(_ context.Context, pid, i int, name string) error {
			selected = append(selected, name)
			return nil
		},
	}
	ctx := context.Background()
	app, vs, err := s.Views(ctx, "")
	if err != nil || app != "libreoffice-calc" || len(vs) != 2 || vs[1].Name != "Summary" {
		t.Fatalf("focused: %s %v %v", app, vs, err)
	}
	app, vs, err = s.Views(ctx, "Firefox")
	if err != nil || app != "firefox" || !vs[1].Selected {
		t.Fatalf("named: %s %v %v", app, vs, err)
	}
	if err := s.Switch(ctx, "firefox", desktop.View{Index: 0, Name: "Claude"}); err != nil || !slices.Equal(selected, []string{"Claude"}) {
		t.Fatalf("switch: %v %v", err, selected)
	}
	// The terminal has the focus and no tabs: the front window that has some is read.
	s.Windows = wins{
		{App: "foot", PID: 30, Title: "~", Focused: true},
		{App: "firefox", PID: 10, Title: "YouTube — Mozilla Firefox"},
	}
	selected = nil
	if app, vs, err := s.Views(ctx, ""); err != nil || app != "firefox" || len(vs) != 2 {
		t.Fatalf("over a terminal: %s %v %v", app, vs, err)
	}
	if err := s.Switch(ctx, "", desktop.View{Index: 0, Name: "Claude"}); err != nil || !slices.Equal(selected, []string{"Claude"}) {
		t.Fatalf("switch over a terminal: %v %v", err, selected)
	}
	if _, _, err := s.Views(ctx, "excel"); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("no excel: %v", err)
	}
}

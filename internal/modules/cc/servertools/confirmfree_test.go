package servertools

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

type plainTool struct{}

func (plainTool) Name() string                                     { return "plain" }
func (plainTool) Definition() *pyjson.Object                       { return pyjson.NewObject() }
func (plainTool) Execute(context.Context, Call, Turn) (any, error) { return nil, nil }

type declaringTool struct {
	plainTool
	free bool
}

func (d declaringTool) RunWithoutConfirmation() bool { return d.free }

func TestRunsWithoutConfirmation(t *testing.T) {
	if !RunsWithoutConfirmation(declaringTool{free: true}) {
		t.Error("tool returning true: want true")
	}
	if RunsWithoutConfirmation(declaringTool{free: false}) {
		t.Error("tool returning false: want false")
	}
	if RunsWithoutConfirmation(plainTool{}) {
		t.Error("tool without the method: want false")
	}
}

func TestDeclaredServerTools(t *testing.T) {
	for _, tool := range []ServerTool{&QuickSearch{}, &DeepResearch{}, &HAEntities{}} {
		if !RunsWithoutConfirmation(tool) {
			t.Errorf("%s: want true", tool.Name())
		}
	}
	for _, tool := range []ServerTool{&RequestValidation{}, &IdentifySpeaker{}} {
		if RunsWithoutConfirmation(tool) {
			t.Errorf("%s: want false", tool.Name())
		}
	}
}

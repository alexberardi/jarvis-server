package computer

import (
	"context"
	"errors"
	"slices"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/models"
)

// computer.model values (llm.SlotChoices).
const (
	ModelAuto       = llm.SlotAuto
	ModelLive       = llm.LabelLive
	ModelBackground = llm.LabelBackground
	ModelCloud      = llm.LabelCloud
)

// ErrNoCloud is pickLabel's error when computer.model is cloud and the cloud slot isn't set up.
var ErrNoCloud = errors.New("No cloud model is set up")

// Models is what the loop knows about the llm slots. cc fills it from its Module (piece 11).
type Models struct {
	// Endpoint resolves a slot ("live", "background", "cloud") for its model and capabilities.
	Endpoint func(ctx context.Context, label string) (llm.Endpoint, error)
	// VisionLabel is the slot a background caller sends images to (cc's visionLabel).
	VisionLabel func(ctx context.Context) (string, bool)
	// Tags are a model's catalogue tags (nil: the built-in catalogue).
	Tags func(modelID string) []string
}

// Pick is the slot a run's model calls go to.
type Pick struct {
	Label   string
	Model   string // the slot's model id ("" when unknown)
	Vision  bool   // the slot accepts images
	Adapter computeruse.Adapter
}

// pickLabel chooses the slot for a computer task from computer.model (mode): live, background
// or cloud name the slot, and cloud fails with ErrNoCloud when it isn't set up. auto (or anything
// else) never picks cloud: it takes the vision slot whose model the catalogue tags "large", else
// the background vision slot (visionLabel), else background without images.
func pickLabel(ctx context.Context, m Models, mode string) (Pick, error) {
	ep := func(label string) (llm.Endpoint, bool) {
		if m.Endpoint == nil {
			return llm.Endpoint{}, false
		}
		e, err := m.Endpoint(ctx, label)
		return e, err == nil
	}
	pick := func(label string) Pick {
		e, _ := ep(label)
		return Pick{Label: label, Model: e.Model, Vision: e.Vision, Adapter: computeruse.AdapterFor(e.Model)}
	}
	switch mode {
	case ModelCloud:
		if _, ok := ep(ModelCloud); !ok {
			return Pick{}, ErrNoCloud
		}
		return pick(ModelCloud), nil
	case ModelLive, ModelBackground:
		return pick(mode), nil
	}
	labels := []string{llm.LabelBackground, llm.LabelLive}
	tags := m.Tags
	if tags == nil {
		tags = catalogTags
	}
	for _, l := range labels {
		if e, ok := ep(l); ok && e.Vision && slices.Contains(tags(e.Model), "large") {
			return pick(l), nil
		}
	}
	if m.VisionLabel != nil {
		if l, ok := m.VisionLabel(ctx); ok {
			return pick(l), nil
		}
	}
	p := pick(llm.LabelBackground)
	p.Vision = false
	return p, nil
}

func catalogTags(modelID string) []string {
	e, ok := models.CatalogEntry(modelID)
	if !ok {
		return nil
	}
	return e.Tags
}

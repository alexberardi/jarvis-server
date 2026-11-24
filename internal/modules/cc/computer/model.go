package computer

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"

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
	Fit     Fit // how well the model is expected to carry a computer task
}

// Fit is how a model is expected to do at computer tasks, from its size. The suite on
// 2026-11-15/16: Gemma 4 26B passed 53 of 57 runs as a conversation, Qwen3.5 9B 40 to 42 of 57
// in any loop, failing the longer errands.
type Fit string

const (
	FitLarge   Fit = "large" // the catalogue's "large" models, 20B and up, and the cloud slot
	FitSmall   Fit = "small" // anything known to be smaller
	FitUnknown Fit = ""      // a model whose size can't be told: treated as small, and said so
	largeFromB     = 20.0    // billions of parameters, read from a model id like "foo-26b"
)

// turnsFromB is the size from which a model that isn't large still works as a conversation.
// Gemma 4 12B waited on a loaded page round after round in the batch loop and finished the
// same task in six as a conversation (one run each, 2026-11-17); the 9B is not moved on that.
const turnsFromB = 12.0

// conversational reports whether a model's id names a size of turnsFromB or more.
func conversational(model string) bool {
	m := sizeInID.FindStringSubmatch(model)
	if m == nil {
		return false
	}
	b, err := strconv.ParseFloat(m[1], 64)
	return err == nil && b >= turnsFromB
}

// sizeInID is the parameter count in a model id: "gemma-4-26b-a4b" is 26, "qwen3.5-9b" is 9.
var sizeInID = regexp.MustCompile(`(?i)(?:^|[-_:/])(\d+(?:\.\d+)?)b(?:[-_:.]|$)`)

// fitOf is the model's Fit: the cloud slot and the catalogue's "large" models are large, any
// other catalogue model is small, and a model the catalogue doesn't know goes by the size in
// its id, or is unknown without one.
func fitOf(label, model string, tags []string) Fit {
	switch {
	case label == ModelCloud, slices.Contains(tags, "large"):
		return FitLarge
	case len(tags) > 0:
		return FitSmall
	}
	if m := sizeInID.FindStringSubmatch(model); m != nil {
		if b, err := strconv.ParseFloat(m[1], 64); err == nil {
			if b >= largeFromB {
				return FitLarge
			}
			return FitSmall
		}
	}
	return FitUnknown
}

// SettingModelSize says how large the computer model is when Jarvis can't tell (a remote
// endpoint that names its model by a label): auto (from the catalogue or the model's id), large
// or small.
const SettingModelSize = "computer.model_size"

// FitOf is the Fit of a model by its id alone, as the catalogue and the id tell it.
func FitOf(model string) Fit { return fitOf("", model, catalogTags(model)) }

// fitNote is what a failed task adds for the user when its model isn't a large one ("" when it is).
func fitNote(f Fit, model string) string {
	const better = "A larger model (about 26B and up) or the cloud model does better: choose it under Models, Computer control."
	switch f {
	case FitLarge:
		return ""
	case FitSmall:
		return fmt.Sprintf("This ran on %s, a small model for computer tasks: short ones in one app usually work, longer ones often don't. %s", model, better)
	}
	if model == "" {
		model = "a model"
	}
	return fmt.Sprintf("This ran on %s, and I can't tell how well it does at computer tasks: smaller models often can't finish longer ones. %s", model, better)
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
	tags := m.Tags
	if tags == nil {
		tags = catalogTags
	}
	pick := func(label string) Pick {
		e, _ := ep(label)
		return Pick{Label: label, Model: e.Model, Vision: e.Vision, Adapter: computeruse.AdapterFor(e.Model),
			Fit: fitOf(label, e.Model, tags(e.Model))}
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

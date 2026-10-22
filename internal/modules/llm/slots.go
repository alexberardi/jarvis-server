package llm

import (
	"fmt"
	"slices"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// SlotAuto is a feature's slot choice that leaves the slot to the feature: its own default,
// which never picks cloud.
const SlotAuto = "auto"

// SlotChoices are the values of a feature's slot setting (computer.model; the planner and
// summaries later): decisions/2026-10-22-cloud-model-slot.md.
var SlotChoices = []string{SlotAuto, LabelLive, LabelBackground, LabelCloud}

// SlotSetting is the definition of a feature's slot setting: a string of SlotChoices, auto by
// default.
func SlotSetting(key, category, description string) settings.Definition {
	opts := make([]any, len(SlotChoices))
	for i, c := range SlotChoices {
		opts[i] = c
	}
	return settings.Definition{Key: key, Category: category, Type: settings.String, Default: SlotAuto,
		Description: description, Options: opts, Validate: validSlot}
}

func validSlot(v any) error {
	if s, ok := v.(string); ok && slices.Contains(SlotChoices, s) {
		return nil
	}
	return fmt.Errorf("must be one of %v", SlotChoices)
}

// chatLabel is the label an in-process ChatRequest goes to: cloud when named exactly, else live
// or background (NormalizeLabel). The HTTP API's requests carry echoLabel's live or background,
// so no API caller reaches cloud.
func chatLabel(label string) string {
	if label == LabelCloud {
		return LabelCloud
	}
	return NormalizeLabel(label)
}

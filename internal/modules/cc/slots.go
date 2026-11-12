package cc

import (
	"context"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Slot settings: which model each cc feature runs on (llm.SlotSetting,
// docs/llm/SPEC-feature-models.md). auto is each feature's own default, the label the code
// hardcoded before these settings existed, and never cloud.
const (
	SettingConversationModel = "conversation.model"
	SettingMemoryModel       = "memory.model"
	SettingSignalsModel      = "signals.model"
	SettingWatchesModel      = "watches.model"
	SettingWorkflowsModel    = "workflows.model"
	SettingResearchModel     = "research.model"
)

func slotDefinitions() []settings.Definition {
	return []settings.Definition{
		llm.SlotSetting(SettingConversationModel, "conversation",
			"Which model answers a chat or voice turn: auto (live), live, background or cloud (the optional "+
				"cloud slot: what you say and the context of the turn go to its provider). Someone is waiting "+
				"on every one of these, so a slow slot is felt directly."),
		llm.SlotSetting(SettingMemoryModel, "memory",
			"Which model extracts memories from a finished conversation: auto (background), live, background "+
				"or cloud (the optional cloud slot: the conversation goes to its provider). It runs after the "+
				"fact, so a slower, larger slot costs nothing but electricity."),
		llm.SlotSetting(SettingSignalsModel, "signals",
			"Which model decides a routine's reaction: auto (background), live, background or cloud (the "+
				"optional cloud slot: the signal and your household context go to its provider)."),
		llm.SlotSetting(SettingWatchesModel, "watches",
			"Which model reads a watched page and decides whether it changed: auto (background), live, "+
				"background or cloud (the optional cloud slot: the page's contents go to its provider)."),
		llm.SlotSetting(SettingResearchModel, "research",
			"Which model writes up a deep-research run: auto (background), live, background or cloud (the "+
				"optional cloud slot: your query and the pages it found go to its provider). It runs as a "+
				"job, so a slower, larger slot is free."),
		llm.SlotSetting(SettingWorkflowsModel, "workflows",
			"Which model learns a workflow from what you did: auto (background), live, background or cloud "+
				"(the optional cloud slot: the steps and what they touched go to its provider)."),
	}
}

// conversationSlot is the label a chat or voice turn goes to: the conversation feature's slot,
// live by default. The tool-result continue, the refine pass, warm-up probes and the context
// window compaction measures all follow the turn onto it.
func (m *Module) conversationSlot(ctx context.Context) string {
	return llm.SlotFor(ctx, m.settings, SettingConversationModel, llm.LabelLive)
}

// featureSlot is the label one of cc's background features runs on.
func (m *Module) featureSlot(ctx context.Context, key, fallback string) string {
	return llm.SlotFor(ctx, m.settings, key, fallback)
}

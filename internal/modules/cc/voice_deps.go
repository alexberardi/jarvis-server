package cc

import (
	"context"
	"database/sql"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Phase 5b: the voice pipeline and the tool loop (docs/cc/01, 02, 06). Every dependency is
// another jarvisd module called in process; a nil dependency makes the routes that need it
// answer 503 (or, for best-effort lookups, degrade the way legacy did on an HTTP failure).

// LLM is the llm module's in-process Service (Chat on the live label for the voice loop,
// Stream for the continue-stream path, Embed for memory vectors, LD6).
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
	Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Frame, error)
	Embed(ctx context.Context, texts []string) (llm.Embeddings, error)
}

// ReadyWait bounds how long a turn waits for a model that is still loading (A10 F22: for
// 5-10 s after a restart or upgrade every turn got 503 model_not_loaded). Nodes give up on a
// voice request after 30 s (jarvis-node-setup's command-center client), so the wait stays
// well under that: the node gets the "still loading" answer, not its own timeout.
const ReadyWait = 15 * time.Second

// WaitingLLM wraps the llm Service so cc's calls (voice, chat, background jobs) wait up to
// wait for a loading label instead of failing at once; on timeout the error says it was
// still loading. The llm HTTP API is unaffected.
func WaitingLLM(inner LLM, wait time.Duration) LLM { return waitingLLM{inner, wait} }

type waitingLLM struct {
	inner LLM
	wait  time.Duration
}

func (w waitingLLM) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return w.inner.Chat(llm.WithReadyWait(ctx, w.wait), req)
}

func (w waitingLLM) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Frame, error) {
	return w.inner.Stream(llm.WithReadyWait(ctx, w.wait), req)
}

func (w waitingLLM) Embed(ctx context.Context, texts []string) (llm.Embeddings, error) {
	return w.inner.Embed(llm.WithReadyWait(ctx, w.wait), texts)
}

// STT is the stt module in process: transcription with the speaker pass, and the voice-profile
// store the node enrollment flow writes to.
type STT interface {
	Transcribe(ctx context.Context, wav []byte, opts stt.TranscribeOptions) (stt.Result, error)
	RecognitionEnabled(ctx context.Context, householdID string) bool
	Enroll(ctx context.Context, householdID string, userID int64, wav []byte, sampleIndex *int) (stt.EnrollResult, error)
	Verify(ctx context.Context, householdID string, userID int64, wav []byte) (stt.VerifyResult, error)
	// Samples and DeleteProfile back the mobile voice-profile routes (06 V1/V6).
	Samples(ctx context.Context, householdID string, userID int64) ([]stt.Sample, error)
	DeleteProfile(ctx context.Context, householdID string, userID int64) error
	// VoiceOf embeds a clip's speech for the follow-up gate's same-voice comparison.
	VoiceOf(ctx context.Context, wav []byte) (stt.Voice, error)
}

// TTS is the tts module in process (TTSFrom adapts *tts.Module).
type TTS interface {
	// Speak renders text; the stream yields PCM in AudioFormat, sentence by sentence.
	Speak(ctx context.Context, text string) (Speech, error)
	AudioFormat() tts.Format
}

// Speech is one synthesis: Next returns PCM chunks until io.EOF.
type Speech interface {
	Next() ([]byte, error)
	Close() error
}

// TTSFrom adapts the tts module.
func TTSFrom(m *tts.Module) TTS { return ttsModule{m} }

type ttsModule struct{ m *tts.Module }

func (t ttsModule) Speak(ctx context.Context, text string) (Speech, error) {
	st, err := t.m.Speak(ctx, text, tts.SpeakOptions{})
	if err != nil {
		return nil, err
	}
	return st, nil
}

func (t ttsModule) AudioFormat() tts.Format { return t.m.AudioFormat() }

// Notifier is the notifications module in process (D31): inbox rows and pushes.
type Notifier interface {
	CreateInboxItem(ctx context.Context, tx *sql.Tx, in notifications.NewInboxItem) (notifications.InboxItem, error)
	Notify(ctx context.Context, tx *sql.Tx, source string, n notifications.Notification) (notifications.Delivery, error)
}

// NameResolver resolves user ids to display names (the auth module's UserNames; legacy
// /internal/users/batch). Failures mean "no name", never an error to the turn.
type NameResolver interface {
	UserNames(ctx context.Context, ids []int64) (map[int64]string, error)
}

// MemoryProfile is the 5c memory module's per-turn profile text for a speaker (the User Profile
// block, D43). Until memory lands it is nil and an identified speaker gets no memories.
type MemoryProfile interface {
	ProfileText(ctx context.Context, userID int64, householdID string) string
}

// AttentionGate is the 5c attention broker's interposition on the node notification routes
// (D18). Nil (and attention.enabled off) is the legacy delivery, byte-identical.
type AttentionGate interface {
	// Gate decides one notification. ok=false means the broker is off or errored: deliver as
	// legacy (fail open).
	Gate(ctx context.Context, req AttentionRequest) (d AttentionDecision, ok bool)
	// Outcome stamps what happened to an approved delivery.
	Outcome(ctx context.Context, deliveryID, outcome, inboxItemID string)
}

// AttentionRequest is one gated notification (node_commands._attention_gate's arguments).
type AttentionRequest struct {
	HouseholdID   string
	OriginNodeID  string
	Source        string
	Category      string
	Title         string
	Summary       string
	RequestedRung string // "push" | "inbox"
	DedupeKey     string
	TargetUserID  *int64
	Payload       map[string]any
	Force         bool
}

// AttentionDecision is the broker's verdict.
type AttentionDecision struct {
	Deliver    bool
	Rung       string // "push" | "inbox"
	WithheldBy string
	DeliveryID string
}

// Setting keys the voice pipeline reads (D11: only keys something reads are declared).
const (
	settingPromptProvider    = "llm.prompt_provider"
	settingToolCalling       = "llm.tool_calling"
	settingThinkingSwitch    = "llm.thinking_switch"
	settingForceTools        = "llm.force_tools"
	settingMaxTurns          = "conversation.max_turns"
	settingIncludeThinking   = "model.include_thinking"
	settingSmallModelMode    = "model.small_model_mode"
	settingMemoryEnabled     = "memory.enabled"
	settingRecallEnabled     = "memory.recall_enabled"
	settingExtractionEnabled = "memory.extraction_enabled"
	settingPersona           = "persona.household_prompt"
	settingWebSearch         = "web_search.enabled"
	settingWakeMode          = "voice.wake_verification_mode"
	settingWakePhrase        = "voice.wake_verification_phrase"
	settingSTTPrompt         = "voice.stt_prompt"
	settingSTTPreprocess     = "voice.stt_preprocess"
	settingDoubtMaxRounds    = "voice.followup_doubt_max_rounds"
	settingDedupeWindow      = "voice.tool_dedupe_window_seconds"
	settingHouseholdLocation = "household.location"
	settingAttention         = "attention.enabled"
	settingWakeArbitration   = "voice.wake_arbitration_window_s"
)

// voiceDefinitions are 5b's settings (legacy services/settings_definitions.py, D11-cleaned).
func voiceDefinitions(defaultPersona string) []settings.Definition {
	return []settings.Definition{
		{Key: settingPromptProvider, Category: "llm", Type: settings.String, Default: "", RequiresReload: false,
			Description: "Default built-in prompt for households without their own (docs/cc/prompt-contract.md; " +
				"D11: renamed from llm.interface; set at install time from the model catalog). One of " +
				"Qwen3_14B_Compressed, Qwen3_8B_Compressed, Qwen3_5_9B_Compressed, ChatGPTOpenAI. An unknown name " +
				"fails /conversation/start."},
		{Key: settingToolCalling, Category: "llm", Type: settings.String, Default: modeAuto,
			Options:  []any{modeAuto, string(prompts.ToolsNative), string(prompts.ToolsText)},
			Validate: oneOf(modeAuto, string(prompts.ToolsNative), string(prompts.ToolsText)),
			Description: "How the live model calls tools: native (tools via the API) or text (<tool_call> in the reply). " +
				"auto uses the household prompt's value, else the model's chat template."},
		{Key: settingThinkingSwitch, Category: "llm", Type: settings.String, Default: modeAuto,
			Options:  []any{modeAuto, thinkingSuffix, thinkingNone},
			Validate: oneOf(modeAuto, thinkingSuffix, thinkingNone),
			Description: "How thinking on/off reaches the model: suffix appends /think or /no_think to the user turn. " +
				"auto uses the household prompt's value, else the chat template."},
		{Key: settingForceTools, Category: "llm", Type: settings.String, Default: modeAuto,
			Options:  []any{modeAuto, forceOn, forceOff},
			Validate: oneOf(modeAuto, forceOn, forceOff),
			Description: "The force-tools guard: a reply that claims an action with no tool call gets one retry asking for " +
				"the call. auto: on for a model this install serves itself, off for a remote one. on/off choose it " +
				"(the harness's sandbox turns it on: its remote label is the installed jarvisd's local model)."},
		{Key: settingMaxTurns, Category: "conversation", Type: settings.Int, Default: int64(10),
			Description: "Maximum user/assistant exchanges kept in conversation history (sliding window — oldest " +
				"turns are dropped first, atomically with their tool calls/results; keeps prompts bounded for the LLM)"},
		{Key: settingIncludeThinking, Category: "model", Type: settings.Bool, Default: false,
			Description: "Allow the model to emit chain-of-thought (<think>) before answering (/think vs /no_think on " +
				"Qwen3). Improves hard queries but adds significant latency (~500 tokens, several seconds), so it defaults off."},
		{Key: settingSmallModelMode, Category: "model", Type: settings.Bool, Default: true,
			Description: "Optimize prompts for smaller models (one invalid-parameter retry instead of two)"},
		{Key: settingMemoryEnabled, Category: "memory", Type: settings.Bool, Default: true,
			Description: "Master toggle for remember/forget/recall tools and for learning from voice (D19)"},
		{Key: settingRecallEnabled, Category: "memory", Type: settings.Bool, Default: true,
			Description: "Enable semantic search via the recall tool"},
		{Key: settingExtractionEnabled, Category: "memory", Type: settings.Bool, Default: true,
			Description: "Learn from voice: log transcripts of turns from a confidently identified speaker and " +
				"extract memories from them (D19/D50; needs speaker recognition on)"},
		{Key: settingPersona, Category: "persona", Type: settings.String, Default: defaultPersona,
			Description: "Household speaking-voice persona injected into the voice prompt as a fenced <personality> " +
				"block. Shapes tone and word choice ONLY — never tool-calling or safety, which stay non-overridable. " +
				"Per-household; editable from mobile (starter presets + free text)."},
		{Key: settingWebSearch, Category: "web_search", Type: settings.Bool, Default: false,
			Description: "Master toggle for web search. Gates the live quick_search lookups and deep_research tools, " +
				"which make outbound requests to the internet. Default OFF so local-only households never egress."},
		{Key: settingWakeMode, Category: "voice", Type: settings.String, Default: "off",
			Description: "Wake-clip verification: 'off', 'bias' (an unverified wake adds a mild misfire-leaning hint) " +
				"or 'enforce' (an unverified wake is silently dropped). Unknown values mean 'off'."},
		{Key: settingWakePhrase, Category: "voice", Type: settings.String, Default: "jarvis",
			Description: "The word the wake clip must (fuzzily, edit distance 2) contain to count as verified."},
		{Key: settingSTTPrompt, Category: "voice", Type: settings.String, Default: "",
			Description: "Whisper initial prompt for node voice commands (e.g. 'Hey Jarvis, turn on the kitchen lights.'): " +
				"biases spelling and vocabulary towards it. Never applied to wake verification, which must stay an " +
				"unbiased check. Empty means none."},
		{Key: settingSTTPreprocess, Category: "voice", Type: settings.Bool, Default: false,
			Description: "Normalise and trim silence from node voice commands before whisper."},
		{Key: settingDoubtMaxRounds, Category: "voice", Type: settings.Int, Default: int64(2),
			Description: "Answered rounds a doubted (unverified-wake) conversation gets before follow-up turns gain a " +
				"strong wrap-up lean."},
		{Key: settingDedupeWindow, Category: "voice", Type: settings.Float, Default: 120.0,
			Description: "Seconds an issued client tool call (name + canonical args) blocks an identical re-issue in the " +
				"same conversation (answered with a one-shot nudge). 0 disables."},
		{Key: settingWakeArbitration, Category: "voice", Type: settings.Float, Default: 1.5,
			Description: "Seconds within which wake turns from several nodes of the household with the same request are " +
				"one \"Hey Jarvis\": one node answers, the others stand down silently (arbitration.go). Max 10. 0 disables."},
		{Key: settingHouseholdLocation, Category: "household", Type: settings.String, Default: "",
			Description: "Household locality used to disambiguate business searches (e.g. 'Springfield, IL'). Sent to " +
				"nodes as home_context.location. Empty means unset."},
		{Key: settingAttention, Category: "attention", Type: settings.Bool, Default: false,
			Description: "Route node notifications through the attention broker (journal, dedup, budgets, quiet hours)."},
	}
}

// Package errands is command-center's errand runner and workflow engine (docs/cc/09): a spoken
// goal becomes an LLM-planned draft (cc_errand_plans) reviewed on a plan card; Run spawns a run
// (cc_workflows) that dispatches one step at a time to its plane (phone call, server tool,
// node command), suspends on deferred steps, and reports with one completion card.
//
// Every asynchronous piece is a durable queue job (D27, LD8): planning, each drive/resume of a
// run, the checkpoint replan, and the deadlines (draft TTL, approval wait, phone wait). Nothing
// is a detached goroutine or a polling loop, so a promised card survives a restart.
package errands

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// LLM is the llm module in process (planning and compose run on the background label).
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}

// Tools is the server-tool registry (*servertools.Registry).
type Tools interface {
	Get(name string) (servertools.ServerTool, bool)
	Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) any
}

// Settings reads the cc settings (*settings.Service).
type Settings interface {
	Bool(ctx context.Context, key string, sc settings.Scope) bool
	Int(ctx context.Context, key string, sc settings.Scope) int64
}

// Nodes is the node data plane (the cc Bus), headless.
type Nodes interface {
	// RunTool runs one command on a node through the tool_call verb and waits up to timeout
	// for its output dict. A publish failure or timeout returns a synthetic failure dict, never
	// nil (dispatch_node_command).
	RunTool(ctx context.Context, nodeID, command string, args *pyjson.Object, userID *int64, voiceCommand string, timeout time.Duration) *pyjson.Object
	// ReportCommands asks a node for its available commands (report_tools). ok=false when it
	// didn't answer.
	ReportCommands(ctx context.Context, nodeID string, timeout time.Duration) ([]*pyjson.Object, bool)
}

// Card is one inbox card (post_inbox_item_sync's arguments).
type Card struct {
	HouseholdID string
	UserID      *int64
	Title       string
	Summary     string
	Body        string
	Category    string
	Metadata    map[string]any
	Push        bool
	TargetType  string // "user" | "household"
}

// Cards posts and updates inbox cards through the notify service (D31).
type Cards interface {
	// Post creates the card (and its push); "" on failure.
	Post(ctx context.Context, c Card) string
	// Update changes a card in place; false when it is gone or the update failed, so the caller
	// posts a fresh one.
	Update(ctx context.Context, itemID string, c Card) bool
}

// Service is the errand runner.
type Service struct {
	DB       *db.DB
	Queue    *queue.Queue
	Log      *slog.Logger
	LLM      LLM
	Tools    Tools
	Settings Settings
	Nodes    Nodes
	Cards    Cards
	// Phone is the phone module (doc 11). Nil: make_phone_call is never offered and a call step
	// fails "couldn't set up the call".
	Phone PhoneCalls
	// Schedules is the schedules store (doc 08). Nil: schedule_errand and list_scheduled_errands
	// refuse.
	Schedules Schedules
	// Timezone is the household's IANA zone (D18; "" or nil = UTC).
	Timezone func(ctx context.Context, householdID string) string
	Now      func() time.Time

	// Tunables (tests shorten them).
	NodeTimeout      time.Duration // node step wait (30 s)
	MenuTimeout      time.Duration // report_tools wait (10 s)
	DraftTTL         time.Duration // 24 h (D40 Q6)
	ApprovalDeadline time.Duration // 24 h (D14)
	PhoneDeadline    time.Duration // 60 min from confirm (D40 Q6)

	driving sync.Map // workflow id → struct{}: runs this process is driving right now
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (s *Service) defaults() {
	if s.NodeTimeout <= 0 {
		s.NodeTimeout = 30 * time.Second
	}
	if s.MenuTimeout <= 0 {
		s.MenuTimeout = 10 * time.Second
	}
	if s.DraftTTL <= 0 {
		s.DraftTTL = 24 * time.Hour
	}
	if s.ApprovalDeadline <= 0 {
		s.ApprovalDeadline = 24 * time.Hour
	}
	if s.PhoneDeadline <= 0 {
		s.PhoneDeadline = 60 * time.Minute
	}
}

func (s *Service) timezone(ctx context.Context, hh string) string {
	if s.Timezone == nil {
		return "UTC"
	}
	if tz := s.Timezone(ctx, hh); tz != "" {
		return tz
	}
	return "UTC"
}

// Setting keys owned by errands.
const (
	SettingEnabled    = "errands.enabled"
	SettingAutonomous = "errands.autonomous_enabled"
	// SettingPlannerMaxTokens caps one planning call's output, thinking included.
	SettingPlannerMaxTokens = "errands.planner_max_tokens"
)

// Definitions are the errand settings (D40 Q12, D9).
func Definitions() []settings.Definition {
	return []settings.Definition{
		{Key: SettingEnabled, Category: "errands", Type: settings.Bool, Default: true,
			Description: "Offer errands: the run_errand, schedule_errand and list_scheduled_errands voice tools, " +
				"and scheduled errand fires. Off removes the tools from the voice prompt."},
		{Key: SettingAutonomous, Category: "errands", Type: settings.Bool, Default: false,
			Description: "Allow a Signal to AUTORUN a low-blast multi-step plan without a " +
				"tap-to-confirm card (e.g. an upcoming appointment auto-checks drive time " +
				"and sets a leave-by reminder). When off (default), a signal-triggered " +
				"plan is proposed as a card instead of executed. Even when on, only plans " +
				"that pass the plan-start blast gate (allowlisted, non-risky, no outbound " +
				"counterparty, few steps) autorun; anything else falls back to a card. " +
				"Fail-closed — any settings error disables autorun — because it lets a " +
				"background signal originate real writes with no human confirmation."},
		{Key: SettingPlannerMaxTokens, Category: "errands", Type: settings.Int, Default: int64(defaultPlannerMaxTokens),
			Description: "Most tokens one errand planning call may produce, thinking included. Planning needs " +
				"thinking to split multi-step and conditional goals; if the model thinks past this it retries " +
				"once without thinking (a rougher plan). Must fit the background model's context with the " +
				"prompt. Higher = better plans on chatty models, slower to arrive."},
	}
}

// Enabled reports errands.enabled for a household (default on).
func (s *Service) Enabled(ctx context.Context, hh string) bool {
	if s.Settings == nil {
		return true
	}
	return s.Settings.Bool(ctx, SettingEnabled, settings.Scope{HouseholdID: hh})
}

// AutonomousEnabled reports errands.autonomous_enabled (dormant, D9: nothing calls it yet).
func (s *Service) AutonomousEnabled(ctx context.Context, hh string) bool {
	if s.Settings == nil {
		return false
	}
	return s.Settings.Bool(ctx, SettingAutonomous, settings.Scope{HouseholdID: hh})
}

// Available reports whether the runner can work at all (it needs the queue and the database).
func (s *Service) Available() bool { return s != nil && s.Queue != nil && s.DB != nil }

// PurgeUser is the D20 account-deletion hook: the user's plans (drafts and launched
// definitions) are deleted and their queued planning jobs cancelled; runs are activity
// history, kept with user_id → NULL, and a run still waiting is cancelled (nobody is left to
// approve or receive it).
func PurgeUser(ctx context.Context, tx *sql.Tx, userID int64, withQueue bool) error {
	qs := []string{
		`DELETE FROM cc_errand_plans WHERE user_id = ?`,
		`UPDATE cc_workflows SET state = 'cancelled', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		   WHERE user_id = ? AND state = 'waiting'`,
		`UPDATE cc_workflows SET user_id = NULL WHERE user_id = ?`,
	}
	for _, q := range qs {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	if withQueue {
		// Queued planning jobs carry the user in their dedup key (planDedupKey).
		if _, err := tx.ExecContext(ctx, `UPDATE platform_jobs SET state = 'cancelled'
			WHERE state = 'queued' AND type = ? AND substr(dedup_key, 1, length(?)) = ?`,
			JobPlan, planDedupPrefix(&userID), planDedupPrefix(&userID)); err != nil {
			return err
		}
	}
	return nil
}

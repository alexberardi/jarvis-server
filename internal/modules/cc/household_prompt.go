package cc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Prompt sources: a cc_household_prompts row's, plus "default" for a household without one.
const (
	promptSourceBuiltin = "builtin"
	promptSourceImport  = "import"
	promptSourceDefault = "default"
)

// householdPrompt is a household's cc_household_prompts row (docs/cc/prompt-contract.md §8).
// Original is the imported text (NULL for a built-in); Text the household's edit (NULL =
// unedited); ToolCalling and ThinkingSwitch the prompt's own LLM values (NULL = from the model).
type householdPrompt struct {
	Source, Name                                string
	Original, Text, ToolCalling, ThinkingSwitch sql.NullString
	UpdatedAt                                   int64
}

// loadHouseholdPrompt is the household's row, or nil when it has none.
func (m *Module) loadHouseholdPrompt(ctx context.Context, hh string) (*householdPrompt, error) {
	var p householdPrompt
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT source, name, original, text, tool_calling, thinking_switch, updated_at
		FROM cc_household_prompts WHERE household_id = ?`, hh).
		Scan(&p.Source, &p.Name, &p.Original, &p.Text, &p.ToolCalling, &p.ThinkingSwitch, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// saveHouseholdPrompt makes p the household's active prompt (UpdatedAt is set here).
func (m *Module) saveHouseholdPrompt(ctx context.Context, hh string, p householdPrompt) error {
	_, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_household_prompts
		(household_id, source, name, original, text, tool_calling, thinking_switch, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (household_id) DO UPDATE SET source = excluded.source, name = excluded.name,
		original = excluded.original, text = excluded.text, tool_calling = excluded.tool_calling,
		thinking_switch = excluded.thinking_switch, updated_at = excluded.updated_at`,
		hh, p.Source, p.Name, p.Original, p.Text, p.ToolCalling, p.ThinkingSwitch, m.now().Unix())
	return err
}

// deleteHouseholdPrompt drops the household's row: it follows the install default again.
func (m *Module) deleteHouseholdPrompt(ctx context.Context, hh string) error {
	_, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_household_prompts WHERE household_id = ?`, hh)
	return err
}

// resolved is the prompt a household's warmup uses. Original is the text an edit started from
// (the embedded file or the import); Fallback, when set, is why the stored prompt was not used
// and the install default was.
type resolved struct {
	Source, Name, Text, Original string
	Prefs                        prompts.ModePrefs
	Edited                       bool
	Fallback                     string
}

// defaultPromptName is the install default built-in: llm.prompt_provider when set, else the
// live model's.
func (m *Module) defaultPromptName(ctx context.Context) string {
	name := strings.TrimSpace(m.settings.String(ctx, settingPromptProvider, settings.Scope{}))
	if name == "" && m.DefaultPromptProvider != nil {
		name = m.DefaultPromptProvider(ctx)
	}
	return name
}

// resolvePrompt is the household's active prompt. Without a row it is the install default. A
// stored prompt that no longer validates, or names an unknown built-in, never fails the turn:
// it is logged and the default is used, keeping the row's LLM values. Only a missing or unknown
// default is an error (D11).
func (m *Module) resolvePrompt(ctx context.Context, hh string) (resolved, error) {
	name := m.defaultPromptName(ctx)
	if err := prompts.CheckBuiltin(name); err != nil {
		return resolved{}, err
	}
	original, _ := prompts.Builtin(name)
	def := resolved{Source: promptSourceDefault, Name: name, Text: original, Original: original}

	row, err := m.loadHouseholdPrompt(ctx, hh)
	if err != nil {
		return resolved{}, err
	}
	if row == nil {
		return def, nil
	}
	r := resolved{
		Source: row.Source, Name: row.Name, Edited: row.Text.Valid,
		Prefs: prompts.ModePrefs{ToolCalling: row.ToolCalling.String, ThinkingSwitch: row.ThinkingSwitch.String},
	}
	switch row.Source {
	case promptSourceBuiltin:
		r.Original, _ = prompts.Builtin(row.Name)
		if err := prompts.CheckBuiltin(row.Name); err != nil {
			return m.promptFallback(hh, def, r, err), nil
		}
	default: // import
		r.Original = row.Original.String
	}
	r.Text = r.Original
	if row.Text.Valid {
		r.Text = row.Text.String
	}
	if err := prompts.Validate(r.Text); err != nil {
		return m.promptFallback(hh, def, r, err), nil
	}
	return r, nil
}

// promptFallback logs why the household's stored prompt r is not used and returns the default
// with r's LLM values.
func (m *Module) promptFallback(hh string, def, r resolved, err error) resolved {
	m.deps.Log.Warn("cc: household prompt not used; install default instead", "household", hh,
		"source", r.Source, "prompt", r.Name, "default", def.Name, "err", err)
	def.Prefs = r.Prefs
	def.Fallback = fmt.Sprintf("%s prompt %q: %v", r.Source, r.Name, err)
	return def
}

// lastInputs keeps, per node, the inputs its latest warmup rendered the system prompt with
// (the editor's preview).
type lastInputs struct {
	mu sync.Mutex
	m  map[string]prompts.Inputs
}

func (l *lastInputs) set(nodeID string, in prompts.Inputs) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]prompts.Inputs{}
	}
	l.m[nodeID] = in
}

func (l *lastInputs) get(nodeID string) (prompts.Inputs, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	in, ok := l.m[nodeID]
	return in, ok
}

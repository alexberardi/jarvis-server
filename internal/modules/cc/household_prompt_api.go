package cc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The household prompt API (blueprint P2 §3): a household admin reads, edits, resets, picks,
// imports and previews the household's prompt. Every route needs the admin role in the
// household of the path.

// promptNameMax caps an imported prompt's display name (characters).
const promptNameMax = 80

func (m *Module) registerHouseholdPrompt(mux *http.ServeMux) {
	const base = "/api/v0/households/{household_id}/prompt"
	mux.HandleFunc("GET "+base, m.householdAdmin(m.handleGetPrompt))
	mux.HandleFunc("PUT "+base, m.householdAdmin(m.handlePutPrompt))
	mux.HandleFunc("POST "+base+"/reset", m.householdAdmin(m.handleResetPrompt))
	mux.HandleFunc("PUT "+base+"/base", m.householdAdmin(m.handlePutPromptBase))
	mux.HandleFunc("POST "+base+"/import", m.householdAdmin(m.handleImportPrompt))
	mux.HandleFunc("POST "+base+"/preview", m.householdAdmin(m.handlePreviewPrompt))
}

// householdAdmin is m.user plus the admin role in {household_id}.
func (m *Module) householdAdmin(h func(w http.ResponseWriter, r *http.Request, hh string)) http.HandlerFunc {
	return m.user(func(w http.ResponseWriter, r *http.Request, u authn.User) {
		hh := r.PathValue("household_id")
		if err := m.requireRole(r.Context(), u.ID, hh, authn.RoleAdmin); err != nil {
			m.writeErr(w, err)
			return
		}
		h(w, r, hh)
	})
}

// promptView is the GET (and every write's) response.
type promptView struct {
	Source    string             `json:"source"`
	Name      string             `json:"name"`
	Text      string             `json:"text"`
	Original  string             `json:"original"`
	Edited    bool               `json:"edited"`
	Fallback  string             `json:"fallback"`
	Builtins  []string           `json:"builtins"`
	Variables []prompts.Variable `json:"variables"`
	Prefs     struct {
		ToolCalling    string `json:"tool_calling"`
		ThinkingSwitch string `json:"thinking_switch"`
	} `json:"prefs"`
	ToolCalling    ModeSetting `json:"tool_calling"`
	ThinkingSwitch ModeSetting `json:"thinking_switch"`
}

func (m *Module) writePromptView(w http.ResponseWriter, r *http.Request, hh string) {
	ctx := r.Context()
	res, err := m.resolvePrompt(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	v := promptView{
		Source: res.Source, Name: res.Name, Text: res.Text, Original: res.Original, Edited: res.Edited,
		Fallback: res.Fallback, Builtins: prompts.BuiltinNames(), Variables: prompts.Variables,
	}
	v.Prefs.ToolCalling, v.Prefs.ThinkingSwitch = res.Prefs.ToolCalling, res.Prefs.ThinkingSwitch
	_, v.ToolCalling, v.ThinkingSwitch = m.llmMode(ctx, res.Prefs)
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (m *Module) handleGetPrompt(w http.ResponseWriter, r *http.Request, hh string) {
	m.writePromptView(w, r, hh)
}

// templateError writes a prompt text's validation error: 400 {detail, line, variable}.
func templateError(w http.ResponseWriter, err error) {
	var te *prompts.TemplateError
	if !errors.As(err, &te) {
		detail(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"detail": te.Error(), "line": te.Line, "variable": te.Variable})
}

// promptPrefOptions are a prompt's own values per LLM mode setting ("" = from the model).
var promptPrefOptions = map[string][]string{
	"tool_calling":    {string(prompts.ToolsNative), string(prompts.ToolsText)},
	"thinking_switch": {thinkingSuffix, thinkingNone},
}

// promptPref checks a prompt value: "" is NULL (from the model), else one of the options.
// The second result is the 400's detail when v is not allowed.
func promptPref(field, v string) (sql.NullString, string) {
	if v == "" {
		return sql.NullString{}, ""
	}
	for _, o := range promptPrefOptions[field] {
		if v == o {
			return sql.NullString{String: v, Valid: true}, ""
		}
	}
	return sql.NullString{}, fmt.Sprintf("Invalid %s: must be one of %s, or \"\" for the model's", field, strings.Join(promptPrefOptions[field], ", "))
}

// promptRow is the household's row to edit: its own, or a built-in row for the current
// default. A row naming a built-in that no longer exists starts over from the default.
func (m *Module) promptRow(r *http.Request, hh string) (householdPrompt, error) {
	ctx := r.Context()
	row, err := m.loadHouseholdPrompt(ctx, hh)
	if err != nil {
		return householdPrompt{}, err
	}
	if row == nil {
		return householdPrompt{Source: promptSourceBuiltin, Name: m.defaultPromptName(ctx)}, nil
	}
	if row.Source == promptSourceBuiltin && prompts.CheckBuiltin(row.Name) != nil {
		row.Name, row.Text = m.defaultPromptName(ctx), sql.NullString{}
	}
	return *row, nil
}

// rowOriginal is the text a row's edit starts from.
func rowOriginal(p householdPrompt) string {
	if p.Source == promptSourceBuiltin {
		text, _ := prompts.Builtin(p.Name)
		return text
	}
	return p.Original.String
}

// handlePutPrompt saves the household's edit and the prompt's values: an absent value keeps
// the stored one, "" clears it. Text equal to the original is stored as no edit.
func (m *Module) handlePutPrompt(w http.ResponseWriter, r *http.Request, hh string) {
	b, _, ok := readBodyLimit(w, r, false, 4*prompts.MaxPromptBytes)
	if !ok {
		return
	}
	text, _ := b.str("text", true)
	prefs := map[string]*string{}
	for field := range promptPrefOptions {
		if b.has(field) {
			v, _ := b.optStrPtr(field)
			if v == nil {
				v = new(string)
			}
			prefs[field] = v
		}
	}
	if !b.done(w) {
		return
	}
	if err := prompts.Validate(text); err != nil {
		templateError(w, err)
		return
	}
	row, err := m.promptRow(r, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	for field, v := range prefs {
		val, bad := promptPref(field, *v)
		if bad != "" {
			detail(w, http.StatusBadRequest, bad)
			return
		}
		if field == "tool_calling" {
			row.ToolCalling = val
		} else {
			row.ThinkingSwitch = val
		}
	}
	row.Text = sql.NullString{String: text, Valid: text != rowOriginal(row)}
	if err := m.saveHouseholdPrompt(r.Context(), hh, row); err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: household prompt edited", "household", hh, "prompt", row.Name)
	m.writePromptView(w, r, hh)
}

// handleResetPrompt drops the household's edit; the prompt and its values stay.
func (m *Module) handleResetPrompt(w http.ResponseWriter, r *http.Request, hh string) {
	ctx := r.Context()
	row, err := m.loadHouseholdPrompt(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if row != nil && row.Text.Valid {
		row.Text = sql.NullString{}
		if err := m.saveHouseholdPrompt(ctx, hh, *row); err != nil {
			m.internalError(w, err)
			return
		}
		m.deps.Log.Info("cc: household prompt reset", "household", hh, "prompt", row.Name)
	}
	m.writePromptView(w, r, hh)
}

// handlePutPromptBase picks a built-in as the household's prompt (a fresh row: no edit, no
// values of its own). "" deletes the row: the household follows the install default.
func (m *Module) handlePutPromptBase(w http.ResponseWriter, r *http.Request, hh string) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	name, _ := b.str("builtin", true)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	var err error
	if name == "" {
		err = m.deleteHouseholdPrompt(ctx, hh)
	} else {
		if prompts.CheckBuiltin(name) != nil {
			detail(w, http.StatusBadRequest, "Unknown built-in prompt")
			return
		}
		err = m.saveHouseholdPrompt(ctx, hh, householdPrompt{Source: promptSourceBuiltin, Name: name})
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: household prompt picked", "household", hh, "builtin", name)
	m.writePromptView(w, r, hh)
}

// handleImportPrompt makes a text/plain body the household's prompt (source import), with
// ?name= and the optional ?tool_calling= / ?thinking_switch= values.
func (m *Module) handleImportPrompt(w http.ResponseWriter, r *http.Request, hh string) {
	q := r.URL.Query()
	name := strings.TrimSpace(q.Get("name"))
	switch {
	case name == "":
		detail(w, http.StatusBadRequest, "name is required")
		return
	case utf8.RuneCountInString(name) > promptNameMax:
		detail(w, http.StatusBadRequest, fmt.Sprintf("name is longer than %d characters", promptNameMax))
		return
	}
	row := householdPrompt{Source: promptSourceImport, Name: name}
	for _, f := range []struct {
		field string
		dst   *sql.NullString
	}{{"tool_calling", &row.ToolCalling}, {"thinking_switch", &row.ThinkingSwitch}} {
		var bad string
		if *f.dst, bad = promptPref(f.field, q.Get(f.field)); bad != "" {
			detail(w, http.StatusBadRequest, bad)
			return
		}
	}
	// One byte over the cap is enough for Validate to refuse it.
	raw, err := io.ReadAll(io.LimitReader(r.Body, prompts.MaxPromptBytes+1))
	if err != nil {
		detail(w, http.StatusBadRequest, "Could not read the prompt")
		return
	}
	text := string(raw)
	if err := prompts.Validate(text); err != nil {
		templateError(w, err)
		return
	}
	row.Original = sql.NullString{String: text, Valid: true}
	if err := m.saveHouseholdPrompt(r.Context(), hh, row); err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: household prompt imported", "household", hh, "prompt", name, "bytes", len(text))
	m.writePromptView(w, r, hh)
}

// handlePreviewPrompt renders a draft text for one of the household's nodes, with the inputs
// of the node's latest warmup (the persona and the mode as they are now). Without a warmup it
// renders from what the server knows (no client tools, devices or command flags): partial.
func (m *Module) handlePreviewPrompt(w http.ResponseWriter, r *http.Request, hh string) {
	b, _, ok := readBodyLimit(w, r, false, 4*prompts.MaxPromptBytes)
	if !ok {
		return
	}
	text, _ := b.str("text", true)
	nodeID, _ := b.str("node_id", true)
	if !b.done(w) {
		return
	}
	if err := prompts.Validate(text); err != nil {
		templateError(w, err)
		return
	}
	ctx := r.Context()
	n, err := m.nodeByID(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && n.householdID.String != hh) {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	res, err := m.resolvePrompt(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	mode, _, _ := m.llmMode(ctx, res.Prefs)
	persona := parse.PyStrip(m.settings.String(ctx, settingPersona, settings.Scope{HouseholdID: hh}))

	in, cached := m.lastInputs.get(nodeID)
	if cached {
		in.Context.HouseholdPersona = persona
		in.ToolCalling = mode.ToolCalling
	} else {
		room, voiceMode := nodeRoomVoice(n)
		in = prompts.Inputs{
			Context: prompts.Context{Room: room, VoiceMode: voiceMode, HouseholdPersona: persona,
				DateKeys: m.dateVocabulary(), RoomHierarchy: m.roomHierarchy(ctx, hh)},
			Tools:       m.previewServerTools(ctx, hh, mode.Native()),
			ToolCalling: mode.ToolCalling,
		}
	}
	system, _ := prompts.SystemPrompt(text, in, prompts.Characterization{})
	viaAPI := 0
	if mode.Native() {
		viaAPI = len(prompts.NativeTools(in.Tools))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"system_prompt": system,
		"tool_calling":  string(mode.ToolCalling),
		"tools_via_api": viaAPI,
		"user_suffix":   mode.UserMessageSuffix(m.householdBool(ctx, settingIncludeThinking, hh)),
		"partial":       !cached,
	})
}

// previewServerTools is the server tools a voice conversation of the household is offered
// (warmup's gates without a speaker or photos).
func (m *Module) previewServerTools(ctx context.Context, hh string, native bool) []prompts.Tool {
	gates := prompts.ToolGates{
		WebSearch:     m.householdBool(ctx, settingWebSearch, hh),
		SpeakerKnown:  m.STT != nil && m.STT.RecognitionEnabled(ctx, hh),
		MemoryEnabled: m.householdBool(ctx, settingMemoryEnabled, hh),
		RecallEnabled: m.householdBool(ctx, settingRecallEnabled, hh),
		Watches:       m.householdBool(ctx, settingWatchesEnabled, hh),
		Computers:     m.hasComputer(ctx, hh),
		Workflows:     m.hasWorkflows(ctx, hh),
		PhotoTools:    map[string]bool{},
	}
	for n := range m.serverPhotoTools() {
		gates.PhotoTools[n] = true
	}
	defs := m.tools.Definitions(m.gateErrandTools(ctx, hh, prompts.ServerToolNames(native, m.tools.Names(), gates)))
	tools := make([]prompts.Tool, len(defs))
	for i, t := range defs {
		tools[i] = servertools.StripImageMarkers(t)
	}
	return tools
}

// nodeRoomVoice is a node's room and voice mode for the prompt, defaulted when unset.
func nodeRoomVoice(n *nodeRow) (room, voiceMode string) {
	room, voiceMode = prompts.DefaultRoom, prompts.DefaultVoiceMode
	if n == nil {
		return room, voiceMode
	}
	if n.room != "" {
		room = n.room
	}
	if n.voiceMode.Valid && n.voiceMode.String != "" {
		voiceMode = n.voiceMode.String
	}
	return room, voiceMode
}

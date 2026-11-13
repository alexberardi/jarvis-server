package cc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"time"

	dbridge "github.com/alexberardi/jarvis-server/internal/modules/cc/discord"
	dclip "github.com/alexberardi/jarvis-server/internal/modules/cc/discord/clip"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Wiring for the Discord bridge (docs/discord-bridge.md, package cc/discord). The household's
// bot is off until an admin turns discord.enabled on and enters the bot token, server and
// channel in the mobile app (household settings, like the Twilio credentials). Each person then
// links their Discord account from the app (a one-time code sent to the bot with /jarvis link).

// discordSetup builds the bridge's pieces once the settings service exists.
func (m *Module) discordSetup() {
	m.discordStore = &dbridge.Store{DB: m.deps.DB}
	m.discordCodes = &dbridge.Codes{}
	m.discord = &dbridge.Manager{
		Source:  dbridge.SettingsSource{Settings: m.settings},
		Store:   m.discordStore,
		Codes:   m.discordCodes,
		Chat:    m.discordChat,
		STT:     m.discordTranscribe(),
		TTS:     m.discordSpeak(),
		Shrink:  recipes.PrepareVisionPhoto,
		HearWAV: m.discordHearWAV,
		Synth:   m.discordSynth,
		Nodes:   m.discordNodes,
		Log:     m.deps.Log,
	}
}

func (m *Module) startDiscord(ctx context.Context) {
	if m.discord != nil {
		m.discord.Start(ctx)
	}
}

// DiscordNotify sends a reminder or alarm text to the user's Discord DMs if they opted in. It is
// the only message Jarvis starts on its own, and it reports whether one went out.
func (m *Module) DiscordNotify(ctx context.Context, userID int64, text string) bool {
	return m.discord != nil && m.discord.Notify(ctx, userID, text)
}

// discordReminder tells a user on Discord about a reminder, alarm or timer a node pushed, when
// they opted in (off by default; DMs only). Nothing else is ever sent unasked.
func (m *Module) discordReminder(userID *int64, category, title, text string) {
	if userID == nil || m.discord == nil {
		return
	}
	switch category {
	case "reminder", "alarm", "timer":
	default:
		return
	}
	msg := text
	if title != "" && title != text {
		msg = title + ": " + text
	}
	m.tasks.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m.DiscordNotify(ctx, *userID, msg)
	})
}

func (m *Module) discordChat(ctx context.Context, userID int64, hh, nodeID, conv, text string, images []dbridge.Image) (string, string, error) {
	var imgs []any
	for _, im := range images {
		imgs = append(imgs, map[string]any{"mime": im.MediaType, "data": base64.StdEncoding.EncodeToString(im.Data)})
	}
	res, err := m.ChatText(ctx, authn.User{ID: userID, HouseholdID: hh}, ChatTextRequest{
		Message: text, NodeID: nodeID, HouseholdID: hh, ConversationID: conv, Images: imgs})
	return res.Text, res.ConversationID, err
}

func (m *Module) discordNodes(ctx context.Context, hh string) ([]dbridge.NodeInfo, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT node_id, COALESCE(NULLIF(room, ''), node_id) FROM cc_nodes WHERE household_id = ? AND is_active = 1 ORDER BY room, node_id`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dbridge.NodeInfo
	for rows.Next() {
		var n dbridge.NodeInfo
		if err := rows.Scan(&n.ID, &n.Name); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// discordTranscribe is nil (voice clips declined) when the server has no speech to text.
func (m *Module) discordTranscribe() dbridge.Transcribe {
	return func(ctx context.Context, audio []byte, _ string) (string, error) {
		if m.STT == nil {
			return "", errors.New("STT unavailable")
		}
		wav, err := dclip.OggToWAV(audio) // a Discord voice message is Ogg Opus
		if err != nil {
			return "", err
		}
		res, err := m.STT.Transcribe(ctx, wav, stt.TranscribeOptions{})
		return res.Text, err
	}
}

// discordPCM speaks text with the household voice and returns the raw audio.
func (m *Module) discordPCM(ctx context.Context, text string) (pcm []byte, channels, rate int, err error) {
	if m.TTS == nil {
		return nil, 0, 0, errors.New("TTS unavailable")
	}
	text = cleanForTTS(text)
	if text == "" {
		return nil, 0, 0, errors.New("nothing to say")
	}
	st, err := m.TTS.Speak(ctx, text)
	if err != nil {
		return nil, 0, 0, err
	}
	defer st.Close()
	var buf bytes.Buffer
	for {
		b, err := st.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, 0, err
		}
		buf.Write(b)
	}
	f := m.TTS.AudioFormat()
	if f.SampleWidth != 2 {
		return nil, 0, 0, errors.New("the voice is not 16-bit audio")
	}
	return buf.Bytes(), f.Channels, f.SampleRate, nil
}

// discordSpeak is a spoken reply as a Discord voice message (Ogg Opus).
func (m *Module) discordSpeak() dbridge.Speak {
	return func(ctx context.Context, text string) ([]byte, float64, error) {
		pcm, ch, rate, err := m.discordPCM(ctx, text)
		if err != nil {
			return nil, 0, err
		}
		return dclip.PCMToOgg(pcm, ch, rate)
	}
}

// discordSynth is a spoken reply as 20 ms Opus packets for the live voice channel.
func (m *Module) discordSynth(ctx context.Context, text string) ([][]byte, error) {
	pcm, ch, rate, err := m.discordPCM(ctx, text)
	if err != nil {
		return nil, err
	}
	packets, _, err := dclip.PCMToPackets(pcm, ch, rate)
	return packets, err
}

// discordHearWAV is speech to text for the live voice channel (a WAV of one utterance).
func (m *Module) discordHearWAV(ctx context.Context, wav []byte) (string, error) {
	if m.STT == nil {
		return "", errors.New("STT unavailable")
	}
	res, err := m.STT.Transcribe(ctx, wav, stt.TranscribeOptions{})
	return res.Text, err
}

// --- app routes (the mobile app's Discord screen) ---

func (m *Module) registerDiscord(mux *http.ServeMux) {
	const base = "/api/v0/mobile/household/{household_id}/discord"
	mux.HandleFunc("GET "+base, m.user(m.handleDiscordStatus))
	mux.HandleFunc("POST "+base+"/link-code", m.user(m.handleDiscordLinkCode))
	mux.HandleFunc("PUT "+base+"/me", m.user(m.handleDiscordPrefs))
	mux.HandleFunc("DELETE "+base+"/me", m.user(m.handleDiscordUnlink))
}

// handleDiscordStatus tells the app whether the bridge is on and connected, and the caller's link.
func (m *Module) handleDiscordStatus(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx, hh := r.Context(), r.PathValue("household_id")
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	out := map[string]any{
		"enabled": m.settings.Bool(ctx, dbridge.SettingEnabled, settings.Scope{HouseholdID: hh}),
		"online":  m.discord.IsOnline(hh),
		"linked":  false,
	}
	l, ok, err := m.discordStore.ByUser(ctx, hh, u.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if ok {
		out["linked"], out["node_id"], out["voice_replies"], out["proactive"] = true, l.NodeID, l.VoiceReplies, l.Proactive
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleDiscordLinkCode makes the one-time code the person sends to the bot. It needs the bridge
// on, and a node to answer through (the one given, or the household's only active node).
func (m *Module) handleDiscordLinkCode(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx, hh := r.Context(), r.PathValue("household_id")
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	b, _, ok := readBody(w, r, true)
	if !ok {
		return
	}
	nodeID, _ := b.str("node_id", false)
	if !b.done(w) {
		return
	}
	if !m.discord.IsOnline(hh) {
		detail(w, http.StatusConflict, "The Discord bot is not running for this household. An admin needs to turn it on and enter the bot token, server and channel first.")
		return
	}
	nodes, err := m.discordNodes(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	switch {
	case nodeID != "":
		found := false
		for _, n := range nodes {
			found = found || n.ID == nodeID
		}
		if !found {
			detail(w, http.StatusNotFound, "Node "+nodeID+" not found in household "+hh)
			return
		}
	case len(nodes) == 1:
		nodeID = nodes[0].ID
	default:
		detail(w, http.StatusUnprocessableEntity, "Pick the node Jarvis should answer through.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"code": m.discordCodes.New(hh, u.ID, nodeID), "expires_in": int(dbridge.CodeTTL.Seconds())})
}

// handleDiscordPrefs changes the caller's own Discord settings: node, voice replies, reminder DMs.
func (m *Module) handleDiscordPrefs(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx, hh := r.Context(), r.PathValue("household_id")
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	b, _, ok := readBody(w, r, true)
	if !ok {
		return
	}
	var p dbridge.Prefs
	if v, present := b.optStrPtr("node_id"); present {
		p.NodeID = v
	}
	if v, present := b.optStrPtr("voice_replies"); present && v != nil {
		if *v != dbridge.VoiceAuto && *v != dbridge.VoiceAlways && *v != dbridge.VoiceNever {
			b.fail("voice_replies", "Input should be 'auto', 'always' or 'never'")
		}
		p.VoiceReplies = v
	}
	if _, present := b.m["proactive"]; present {
		if v, ok := b.boolean("proactive"); ok {
			p.Proactive = &v
		}
	}
	if !b.done(w) {
		return
	}
	if p.NodeID != nil {
		nodes, err := m.discordNodes(ctx, hh)
		if err != nil {
			m.internalError(w, err)
			return
		}
		found := false
		for _, n := range nodes {
			found = found || n.ID == *p.NodeID
		}
		if !found {
			detail(w, http.StatusNotFound, "Node "+*p.NodeID+" not found in household "+hh)
			return
		}
	}
	linked, err := m.discordStore.SetPrefs(ctx, hh, u.ID, p)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !linked {
		detail(w, http.StatusNotFound, "Your Discord account is not linked yet.")
		return
	}
	m.handleDiscordStatus(w, r, u)
}

func (m *Module) handleDiscordUnlink(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx, hh := r.Context(), r.PathValue("household_id")
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	if err := m.discordStore.Delete(ctx, hh, u.ID); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// purgeDiscordUser forgets a deleted user's links.
func purgeDiscordUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM cc_discord_links WHERE user_id = ?`, userID)
	return err
}

package cc

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/channels"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Chat-service channels (docs/CHANNELS.md, package cc/channels): a household member links
// their account on a service (a provider: a name its package picks) to their Jarvis user with a
// one-time code made in the app and sent to that service's bot. The app's routes are here; the
// provider's daemon has its own. Ported from the Discord bridge of #649, with the provider in
// the path.

// channelsSetup builds the store and the codes.
func (m *Module) channelsSetup() {
	m.channelStore = &channels.Store{DB: m.deps.DB, Now: func() time.Time { return m.now() }}
	m.channelCodes = &channels.Codes{Now: func() time.Time { return m.now() }}
}

func (m *Module) registerChannels(mux *http.ServeMux) {
	const base = "/api/v0/mobile/household/{household_id}/channels"
	mux.HandleFunc("GET "+base, m.user(m.handleChannels))
	mux.HandleFunc("POST "+base+"/{provider}/link-code", m.user(m.handleChannelLinkCode))
	mux.HandleFunc("PUT "+base+"/{provider}/me", m.user(m.handleChannelPrefs))
	mux.HandleFunc("DELETE "+base+"/{provider}/me", m.user(m.handleChannelUnlink))
}

// channelNodes are the household's active nodes a member can have their turns run through.
func (m *Module) channelNodes(ctx context.Context, hh string) (ids []string, err error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT node_id FROM cc_nodes WHERE household_id = ? AND is_active = 1
		ORDER BY room, node_id`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// channelMember is the caller's household and the provider in the path, once the caller is a
// member of that household. ok false: the response is written.
func (m *Module) channelMember(w http.ResponseWriter, r *http.Request, u authn.User, wantProvider bool) (hh, provider string, ok bool) {
	hh = r.PathValue("household_id")
	if err := m.requireRole(r.Context(), u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return "", "", false
	}
	if provider = r.PathValue("provider"); wantProvider && !channels.ValidProvider(provider) {
		detail(w, http.StatusNotFound, "No such channel")
		return "", "", false
	}
	return hh, provider, true
}

// channelEntry is one provider as the app lists it, with the caller's link.
func (m *Module) channelEntry(ctx context.Context, hh string, p channels.Provider, userID int64) (map[string]any, error) {
	out := map[string]any{"provider": p.Name, "online": m.channelStore.Online(p), "linked": false}
	l, ok, err := m.channelStore.ByUser(ctx, hh, p.Name, userID)
	if err != nil {
		return nil, err
	}
	if ok {
		out["linked"], out["node_id"], out["voice_replies"], out["proactive"] = true, l.NodeID, l.VoiceReplies, l.Proactive
	}
	return out, nil
}

// handleChannels lists the services whose daemons have called in for the household: whether
// each is running now, and the caller's link to it.
func (m *Module) handleChannels(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	hh, _, ok := m.channelMember(w, r, u, false)
	if !ok {
		return
	}
	ps, err := m.channelStore.Providers(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	list := []any{}
	for _, p := range ps {
		e, err := m.channelEntry(ctx, hh, p, u.ID)
		if err != nil {
			m.internalError(w, err)
			return
		}
		list = append(list, e)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"channels": list})
}

// channelProvider is the provider in the path as the household knows it; found false when
// its daemon has never called in.
func (m *Module) channelProvider(ctx context.Context, hh, name string) (p channels.Provider, found bool, err error) {
	ps, err := m.channelStore.Providers(ctx, hh)
	for _, p := range ps {
		if p.Name == name {
			return p, true, err
		}
	}
	return channels.Provider{}, false, err
}

// handleChannelLinkCode makes the one-time code the person sends to the service's bot. The
// provider's daemon must be running, and there must be a node to answer through (the one
// given, or the household's only active node).
func (m *Module) handleChannelLinkCode(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	hh, provider, ok := m.channelMember(w, r, u, true)
	if !ok {
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
	p, found, err := m.channelProvider(ctx, hh, provider)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !found || !m.channelStore.Online(p) {
		detail(w, http.StatusConflict, "That channel is not running for this household. Its package has to be installed on a node and set up first.")
		return
	}
	nodes, err := m.channelNodes(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	switch {
	case nodeID != "":
		if !slices.Contains(nodes, nodeID) {
			detail(w, http.StatusNotFound, "Node "+nodeID+" not found in household "+hh)
			return
		}
	case len(nodes) == 1:
		nodeID = nodes[0]
	default:
		detail(w, http.StatusUnprocessableEntity, "Pick the node Jarvis should answer through.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"code": m.channelCodes.New(hh, provider, u.ID, nodeID), "expires_in": int(channels.CodeTTL.Seconds())})
}

// handleChannelPrefs changes the caller's own link: node, voice replies, reminders.
func (m *Module) handleChannelPrefs(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	hh, provider, ok := m.channelMember(w, r, u, true)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, true)
	if !ok {
		return
	}
	var p channels.Prefs
	if v, present := b.optStrPtr("node_id"); present {
		p.NodeID = v
	}
	if v, present := b.optStrPtr("voice_replies"); present && v != nil {
		if *v != channels.VoiceAuto && *v != channels.VoiceAlways && *v != channels.VoiceNever {
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
		nodes, err := m.channelNodes(ctx, hh)
		if err != nil {
			m.internalError(w, err)
			return
		}
		if !slices.Contains(nodes, *p.NodeID) {
			detail(w, http.StatusNotFound, "Node "+*p.NodeID+" not found in household "+hh)
			return
		}
	}
	linked, err := m.channelStore.SetPrefs(ctx, hh, provider, u.ID, p)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !linked {
		detail(w, http.StatusNotFound, "Your account on that channel is not linked yet.")
		return
	}
	known, _, err := m.channelProvider(ctx, hh, provider)
	if err != nil {
		m.internalError(w, err)
		return
	}
	known.Name = provider // a link outlives a provider that never called in again
	e, err := m.channelEntry(ctx, hh, known, u.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

func (m *Module) handleChannelUnlink(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, provider, ok := m.channelMember(w, r, u, true)
	if !ok {
		return
	}
	if err := m.channelStore.Delete(r.Context(), hh, provider, u.ID); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// purgeChannelLinks forgets a user's links: in one household (they left it), or all ("": the
// account is deleted).
func purgeChannelLinks(ctx context.Context, tx *sql.Tx, userID int64, hh string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM cc_channel_links WHERE user_id = ? AND (? = '' OR household_id = ?)`, userID, hh, hh)
	return err
}

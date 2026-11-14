package cc

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/channels"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// What a chat-service channel's daemon calls (docs/CHANNELS.md): a package's daemon on a node
// of the household holds the service's connection and posts here, as that node, what an account
// there sent. jarvisd decides who that account is and answers; the daemon says it in the place
// it was asked. The rules are the Discord bridge's of #649 (its bridge.go): only linked
// accounts get an answer, a conversation is kept per person and place, and nothing here names
// a service.

const (
	// channelTurnTimeout bounds one message's chat turn, which the daemon's request waits for.
	channelTurnTimeout = 3 * time.Minute
	// channelBodyMax is the largest message body: under the 16 MiB line a daemon's request
	// crosses the agent in. Photos are shrunk here to the chat's limit, so a package sends
	// them as it got them when they fit.
	channelBodyMax = 15 << 20
)

// channelConversations remembers each person's conversation in each place, in memory: a
// restart starts fresh ones.
type channelConversations struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *channelConversations) get(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[key]
}

func (c *channelConversations) set(key, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]string{}
	}
	if id == "" {
		delete(c.m, key)
		return
	}
	c.m[key] = id
}

func (m *Module) registerChannelNode(mux *http.ServeMux) {
	const base = "/api/v0/node/channels/{provider}"
	mux.HandleFunc("POST "+base+"/message", m.node(m.handleChannelMessage))
	mux.HandleFunc("POST "+base+"/command", m.node(m.handleChannelCommand))
	mux.HandleFunc("POST "+base+"/outbox", m.node(m.handleChannelOutbox))
}

// channelCall is the start of every daemon call: the provider in the path, noted as alive on
// this node. ok false: the response is written.
func (m *Module) channelCall(w http.ResponseWriter, r *http.Request, n *nodeCtx) (provider string, ok bool) {
	provider = r.PathValue("provider")
	if !channels.ValidProvider(provider) {
		detail(w, http.StatusNotFound, "No such channel")
		return "", false
	}
	if n.HouseholdID == "" {
		detail(w, http.StatusConflict, "This node is in no household")
		return "", false
	}
	if err := m.channelStore.Seen(r.Context(), n.HouseholdID, provider, n.ID); err != nil {
		m.internalError(w, err)
		return "", false
	}
	return provider, true
}

// channelSender reads who sent it and where, and finds their link. linked false with ok true:
// the account isn't linked.
func (m *Module) channelSender(w http.ResponseWriter, r *http.Request, n *nodeCtx, provider string, b *body) (l channels.Link, place string, linked, ok bool) {
	ext, _ := b.str("external_user_id", true)
	place, _ = b.str("place", true)
	b.maxLen("external_user_id", ext, 200)
	b.maxLen("place", place, 200)
	if len(*b.errs) > 0 {
		return channels.Link{}, "", false, true // the caller's done() writes them
	}
	l, linked, err := m.channelStore.ByExternal(r.Context(), n.HouseholdID, provider, ext)
	if err != nil {
		m.internalError(w, err)
		return channels.Link{}, "", false, false
	}
	l.HouseholdID, l.Provider, l.ExternalID = n.HouseholdID, provider, ext
	return l, place, linked, true
}

func channelConvKey(l channels.Link, place string) string {
	return l.HouseholdID + "\x00" + l.Provider + "\x00" + l.ExternalID + "\x00" + place
}

// handleChannelMessage answers what a linked account sent: {"linked": true, "reply": "..."},
// or "failed" with what to tell them. An account that isn't linked gets {"linked": false} and
// the daemon says nothing: strangers are ignored without a word.
func (m *Module) handleChannelMessage(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	provider, ok := m.channelCall(w, r, n)
	if !ok {
		return
	}
	b, _, ok := readBodyLimit(w, r, false, channelBodyMax)
	if !ok {
		return
	}
	l, place, linked, ok := m.channelSender(w, r, n, provider, b)
	if !ok {
		return
	}
	text, _ := b.str("text", false)
	raw, _ := b.m["images"].([]any)
	if _, present := b.m["images"]; present && raw == nil {
		b.fail("images", "Input should be a list")
	}
	if strings.TrimSpace(text) == "" && len(raw) == 0 {
		b.fail("text", "A message needs text or an image")
	}
	if !b.done(w) {
		return
	}
	if !linked {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"linked": false})
		return
	}
	failed := func(msg string) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"linked": true, "failed": msg})
	}
	images, msg := channelImages(raw)
	if msg != "" {
		failed(msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), channelTurnTimeout)
	defer cancel()
	key := channelConvKey(l, place)
	res, err := m.ChatText(ctx, authn.User{ID: l.UserID, HouseholdID: l.HouseholdID}, ChatTextRequest{
		Message: text, NodeID: l.NodeID, HouseholdID: l.HouseholdID, ConversationID: m.channelConvs.get(key), Images: images})
	if err != nil {
		m.deps.Log.Warn("channels: a turn failed", "provider", provider, "user", l.UserID, "err", err)
		failed("Jarvis could not answer that: " + truncateRunes(err.Error(), 300))
		return
	}
	m.channelConvs.set(key, res.ConversationID)
	reply := res.Text
	if reply == "" {
		reply = "(no reply)"
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"linked": true, "reply": reply})
}

// channelImages shrinks a message's photos ({"mime", "data"} as the mobile chat's) to what the
// chat accepts. failure is what to tell the sender.
func channelImages(raw []any) (out []any, failure string) {
	if len(raw) > chatMaxImages {
		return nil, fmt.Sprintf("That is too many photos at once: I can look at %d.", chatMaxImages)
	}
	for _, v := range raw {
		o, _ := v.(map[string]any)
		s, _ := o["data"].(string)
		data, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(data) == 0 {
			return nil, "I couldn't read that image."
		}
		small, ok := recipes.PrepareVisionPhoto(data)
		if !ok {
			return nil, "I couldn't read that image. JPEG, PNG and WebP photos work."
		}
		if len(small) > chatMaxImageBytes {
			return nil, "That image is too big even after shrinking it."
		}
		out = append(out, map[string]any{"mime": "image/jpeg", "data": base64.StdEncoding.EncodeToString(small)})
	}
	return out, ""
}

// handleChannelCommand runs one of the commands every channel has, whatever a person types to
// reach it there: link, new, node, voice, notify, status. The answer is {"text", "private"};
// private is for the asker's eyes only where the service can do that. Unlike a message it
// always answers, because the person asked directly.
func (m *Module) handleChannelCommand(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	provider, ok := m.channelCall(w, r, n)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	l, place, linked, ok := m.channelSender(w, r, n, provider, b)
	if !ok {
		return
	}
	name, _ := b.str("name", true)
	args, _ := b.object("args", false)
	if !b.done(w) {
		return
	}
	arg := func(k string) string { s, _ := args[k].(string); return strings.TrimSpace(s) }
	say := func(s string) { httpx.WriteJSON(w, http.StatusOK, map[string]any{"text": s, "private": true}) }
	const wrong = "Something went wrong, try again."
	ctx := r.Context()

	if name == "link" {
		p, ok, err := m.channelCodes.Redeem(arg("code"), l.HouseholdID, provider, l.ExternalID)
		switch {
		case err != nil:
			say(strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + ".")
		case !ok:
			say("That code is wrong or has expired. Make a new one in the Jarvis app.")
		default:
			if err := m.channelStore.Put(ctx, channels.Link{HouseholdID: l.HouseholdID, Provider: provider,
				ExternalID: l.ExternalID, UserID: p.UserID, NodeID: p.NodeID}); err != nil {
				m.deps.Log.Error("channels: could not save a link", "err", err)
				say(wrong)
				return
			}
			say("Linked. You can talk to me here now. Reminder messages are off; you can turn them on.")
		}
		return
	}
	if !linked {
		say("I don't know this account yet. In the Jarvis app open your channels, make a link code, and send it to me with the link command.")
		return
	}
	switch name {
	case "new":
		m.channelConvs.set(channelConvKey(l, place), "")
		say("Started a fresh conversation.")
	case "node":
		nodes, err := m.channelNodes(ctx, l.HouseholdID)
		if err != nil {
			say(wrong)
			return
		}
		want := arg("name")
		for _, id := range nodes {
			if strings.EqualFold(id, want) {
				if _, err := m.channelStore.SetPrefs(ctx, l.HouseholdID, provider, l.UserID, channels.Prefs{NodeID: &id}); err != nil {
					say(wrong)
					return
				}
				say("I'll answer through " + id + ".")
				return
			}
		}
		say("I don't know that node. Yours are: " + strings.Join(nodes, ", ") + ".")
	case "voice":
		mode := strings.ToLower(arg("mode"))
		if mode != channels.VoiceAuto && mode != channels.VoiceAlways && mode != channels.VoiceNever {
			say("Voice replies can be auto, always or never.")
			return
		}
		if _, err := m.channelStore.SetPrefs(ctx, l.HouseholdID, provider, l.UserID, channels.Prefs{VoiceReplies: &mode}); err != nil {
			say(wrong)
			return
		}
		say("Voice replies: " + mode + ".")
	case "notify":
		state := strings.ToLower(arg("state"))
		if state != "on" && state != "off" {
			say("Reminder messages can be on or off.")
			return
		}
		on := state == "on"
		if _, err := m.channelStore.SetPrefs(ctx, l.HouseholdID, provider, l.UserID, channels.Prefs{Proactive: &on}); err != nil {
			say(wrong)
			return
		}
		if on {
			say("I'll send your reminders here.")
		} else {
			say("I won't message you on my own.")
		}
	case "status":
		pro := "off"
		if l.Proactive {
			pro = "on"
		}
		say(fmt.Sprintf("Linked. Node: %s. Voice replies: %s. Reminder messages: %s.", l.NodeID, l.VoiceReplies, pro))
	default:
		say("I don't know that command.")
	}
}

// handleChannelOutbox is the daemon asking what to send unasked, every few seconds; the call
// is also its sign of life (channelCall). Reminders arrive with K4: until then there is nothing.
func (m *Module) handleChannelOutbox(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	if _, ok := m.channelCall(w, r, n); !ok {
		return
	}
	if _, _, ok := readBody(w, r, true); !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"messages": []any{}})
}

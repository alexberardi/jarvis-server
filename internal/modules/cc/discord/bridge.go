package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Limits on what a Discord message can make the server do.
const (
	maxReply         = 1900 // Discord's limit is 2000 characters
	maxImages        = 4
	maxImageBytes    = 2 << 20  // what the chat accepts after shrinking
	maxRawImage      = 25 << 20 // what is downloaded before shrinking (Discord's own limit)
	maxVoiceBytes    = 8 << 20
	maxVoiceSecs     = 120
	turnTimeout      = 3 * time.Minute
	threadNameLen    = 60
	emojiSeen        = "👀"
	emojiDone        = "✅"
	emojiFailed      = "❌"
	unlinkedHelp     = "I don't know this Discord account yet. In the Jarvis app open Settings, Discord, tap Link, then send me `/jarvis link` with the code it shows."
	wrongPlaceHelp   = "I only answer in direct messages and in the Jarvis channel."
	typedCommandHelp = "That went in as a normal message, so it did not run. Type `/`, pick **jarvis** and then the command from the list Discord shows, and fill in the box. If it had a link code, that code is now visible here: make a new one in the Jarvis app."
)

// Image is a photo to ask about.
type Image struct {
	MediaType string
	Data      []byte
}

// ChatFunc runs one chat turn as a Jarvis user (cc.Module.ChatText behind an adapter).
type ChatFunc func(ctx context.Context, userID int64, hh, nodeID, conversationID, text string, images []Image) (reply, newConversationID string, err error)

// Transcribe turns an audio clip into text.
type Transcribe func(ctx context.Context, audio []byte, contentType string) (string, error)

// Speak turns text into an Ogg Opus clip for a Discord voice message.
type Speak func(ctx context.Context, text string) (ogg []byte, seconds float64, err error)

// Fetch downloads an attachment (the adapter allows only Discord's own hosts and caps the size).
type Fetch func(ctx context.Context, url string, max int64) ([]byte, error)

// NodeInfo is a node a user can pick.
type NodeInfo struct{ ID, Name string }

// Out is everything the bridge does to Discord, so the rules can be tested without it. Nothing
// here ever mentions anyone.
type Out interface {
	React(ctx context.Context, channelID, messageID, emoji string) error
	Typing(ctx context.Context, channelID string) error
	Send(ctx context.Context, channelID, text string) error
	SendVoice(ctx context.Context, channelID string, ogg []byte, seconds float64) error
	StartThread(ctx context.Context, channelID, messageID, name string) (threadID string, err error)
	SendDM(ctx context.Context, discordUserID, text string) error
}

// VoiceControl joins and leaves the Jarvis voice channel (the Discord gateway implements it).
type VoiceControl interface {
	// Join joins the voice channel the person is in, if it is the configured one.
	Join(ctx context.Context, discordUserID string) (string, error)
	Leave(ctx context.Context) string
}

// Attachment is a file on a Discord message.
type Attachment struct {
	URL          string
	ContentType  string
	Size         int64
	VoiceMessage bool
	Seconds      float64
}

// Message is a Discord message the bridge may answer.
type Message struct {
	ID          string
	ChannelID   string
	ParentID    string // the parent channel when ChannelID is a thread
	AuthorID    string
	AuthorBot   bool
	DM          bool
	Content     string
	Attachments []Attachment
}

// Bridge answers one household's Discord bot. It is safe for concurrent use.
type Bridge struct {
	HouseholdID string
	ChannelID   string // the one guild channel it answers in (besides DMs)
	// VoiceChannelID is the configured voice channel. Its own text chat counts as a place too, so
	// /jarvis join can be run right where the person is.
	VoiceChannelID string
	BotUserID      string

	Store *Store
	Codes *Codes
	Chat  ChatFunc
	STT   Transcribe // nil: voice clips are declined
	TTS   Speak      // nil: no spoken replies
	Fetch Fetch
	Nodes func(ctx context.Context, hh string) ([]NodeInfo, error)
	Out   Out
	Log   *slog.Logger

	// Live voice (nil: not available). HearWAV turns speech into text; Synth turns text into 20 ms
	// Opus packets for the channel.
	// Shrink makes a downloaded photo fit the chat's image limit (nil: sent as downloaded).
	Shrink  func(raw []byte) (jpeg []byte, ok bool)
	HearWAV func(ctx context.Context, wav []byte) (string, error)
	Synth   func(ctx context.Context, text string) ([][]byte, error)
	VoiceCh VoiceControl // set by the Discord gateway

	mu    sync.Mutex
	convs map[string]string
}

func (b *Bridge) log() *slog.Logger {
	if b.Log != nil {
		return b.Log
	}
	return slog.Default()
}

// place says where a message or command may be answered: a DM, the Jarvis channel, or a thread in it.
func (b *Bridge) place(dm bool, channelID, parentID string) (ok bool, inChannel bool, source string) {
	switch {
	case dm:
		return true, false, "dm:" + channelID
	case b.ChannelID != "" && channelID == b.ChannelID:
		return true, true, "ch:" + b.ChannelID
	case b.ChannelID != "" && parentID == b.ChannelID:
		return true, false, "ch:" + b.ChannelID // already in a thread of the channel
	case b.VoiceChannelID != "" && channelID == b.VoiceChannelID:
		return true, false, "vc:" + b.VoiceChannelID // the voice channel's own text chat
	}
	return false, false, ""
}

func (b *Bridge) convKey(discordUserID, source string) string { return discordUserID + "|" + source }

func (b *Bridge) conv(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.convs[key]
}

func (b *Bridge) setConv(key, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.convs == nil {
		b.convs = map[string]string{}
	}
	b.convs[key] = id
}

func (b *Bridge) dropConv(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.convs, key)
}

// HandleMessage answers a Discord message if it is from a linked account in a place the bridge
// answers. Everything else is ignored without a word: bots, strangers, other channels.
func (b *Bridge) HandleMessage(ctx context.Context, m Message) {
	if m.AuthorBot || m.AuthorID == b.BotUserID {
		return
	}
	ok, inChannel, source := b.place(m.DM, m.ChannelID, m.ParentID)
	if !ok {
		return
	}
	// "/jarvis link code:..." typed as text, not picked from Discord's command list, arrives as an
	// ordinary message. Say so, to anyone, instead of ignoring it or sending it to Jarvis as chat.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(stripMention(m.Content, b.BotUserID))), "/jarvis") {
		_ = b.Out.Send(ctx, m.ChannelID, typedCommandHelp)
		return
	}
	link, linked, err := b.Store.ByDiscord(ctx, b.HouseholdID, m.AuthorID)
	if err != nil {
		b.log().Error("discord: link lookup failed", "err", err)
		return
	}
	if !linked {
		return
	}

	text := stripMention(m.Content, b.BotUserID)
	var voice *Attachment
	var photos []Attachment
	for i := range m.Attachments {
		a := m.Attachments[i]
		switch {
		case a.VoiceMessage || strings.HasPrefix(a.ContentType, "audio/"):
			if voice == nil {
				voice = &a
			}
		case strings.HasPrefix(a.ContentType, "image/"):
			photos = append(photos, a)
		}
	}
	if text == "" && voice == nil && len(photos) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	_ = b.Out.React(ctx, m.ChannelID, m.ID, emojiSeen)
	replyTo := m.ChannelID
	if inChannel {
		name := text
		if name == "" {
			name = "Jarvis"
		}
		if t, err := b.Out.StartThread(ctx, m.ChannelID, m.ID, clip(name, threadNameLen)); err == nil {
			replyTo = t
		} else {
			b.log().Warn("discord: could not start a thread, replying in the channel", "err", err)
		}
	}
	_ = b.Out.Typing(ctx, replyTo)

	fail := func(msg string) {
		_ = b.Out.React(ctx, m.ChannelID, m.ID, emojiFailed)
		_ = b.Out.Send(ctx, replyTo, msg)
	}

	heard := ""
	if voice != nil {
		t, msg := b.transcribe(ctx, *voice)
		if msg != "" {
			fail(msg)
			return
		}
		heard = t
		if text != "" {
			text += "\n" + t
		} else {
			text = t
		}
	}
	var imgs []Image
	for i, p := range photos {
		if i >= maxImages {
			break
		}
		data, err := b.Fetch(ctx, p.URL, maxRawImage)
		if err != nil {
			b.log().Warn("discord: image download failed", "err", err, "type", p.ContentType, "size", p.Size)
			fail("I couldn't download that image.")
			return
		}
		media := p.ContentType
		if b.Shrink != nil {
			small, ok := b.Shrink(data)
			if !ok {
				fail("I couldn't read that image. JPEG, PNG and WebP photos work.")
				return
			}
			data, media = small, "image/jpeg"
		}
		if len(data) > maxImageBytes {
			fail("That image is too big even after shrinking it.")
			return
		}
		p.ContentType = media
		imgs = append(imgs, Image{MediaType: p.ContentType, Data: data})
	}

	key := b.convKey(m.AuthorID, source)
	reply, cid, err := b.Chat(ctx, link.UserID, b.HouseholdID, link.NodeID, b.conv(key), text, imgs)
	if err != nil {
		b.log().Warn("discord: chat failed", "user", link.UserID, "err", err)
		fail("Jarvis could not answer that: " + clip(err.Error(), 300))
		return
	}
	if cid != "" {
		b.setConv(key, cid)
	}
	if reply == "" {
		reply = "(no reply)"
	}
	if heard != "" {
		reply = "Heard: " + heard + "\n\n" + reply
	}
	for _, chunk := range Split(reply, maxReply) {
		if err := b.Out.Send(ctx, replyTo, chunk); err != nil {
			b.log().Warn("discord: send failed", "err", err)
			return
		}
	}
	if b.TTS != nil && (link.VoiceReplies == VoiceAlways || link.VoiceReplies == VoiceAuto && voice != nil) {
		if ogg, secs, err := b.TTS(ctx, reply); err != nil {
			b.log().Warn("discord: spoken reply failed, the text stands", "err", err)
		} else if err := b.Out.SendVoice(ctx, replyTo, ogg, secs); err != nil {
			b.log().Warn("discord: voice message failed, the text stands", "err", err)
		}
	}
	_ = b.Out.React(ctx, m.ChannelID, m.ID, emojiDone)
}

func (b *Bridge) transcribe(ctx context.Context, a Attachment) (text, failure string) {
	if b.STT == nil {
		return "", "Voice clips are not set up on this server."
	}
	if a.Seconds > maxVoiceSecs || a.Size > maxVoiceBytes {
		return "", fmt.Sprintf("That clip is too long. Keep voice clips under %d seconds.", maxVoiceSecs)
	}
	data, err := b.Fetch(ctx, a.URL, maxVoiceBytes)
	if err != nil {
		return "", "I couldn't download that voice clip."
	}
	t, err := b.STT(ctx, data, a.ContentType)
	t = strings.TrimSpace(t)
	if err != nil || t == "" {
		return "", "I couldn't make out that clip."
	}
	return t, ""
}

// Notify sends a reminder or alarm to the user's DMs, only if they opted in. It is the one way
// Jarvis starts a conversation. It reports whether anything was sent.
func (b *Bridge) Notify(ctx context.Context, userID int64, text string) (bool, error) {
	link, ok, err := b.Store.ByUser(ctx, b.HouseholdID, userID)
	if err != nil || !ok || !link.Proactive {
		return false, err
	}
	var first error
	for _, chunk := range Split(text, maxReply) {
		if err := b.Out.SendDM(ctx, link.DiscordUserID, chunk); err != nil {
			first = err
			break
		}
	}
	return first == nil, first
}

// Command is a slash command, already parsed by the Discord adapter.
type Command struct {
	UserID    string
	ChannelID string
	ParentID  string
	DM        bool
	Name      string
	Args      map[string]string
}

// Reply is a slash command's answer. Ephemeral replies are seen only by the person who asked.
type Reply struct {
	Text      string
	Ephemeral bool
}

// HandleCommand runs a /jarvis command. Unlike messages it always answers, because the person
// asked it something directly.
func (b *Bridge) HandleCommand(ctx context.Context, c Command) Reply {
	priv := func(s string) Reply { return Reply{Text: s, Ephemeral: true} }
	ok, _, source := b.place(c.DM, c.ChannelID, c.ParentID)
	if !ok {
		b.log().Info("discord: command refused, not a DM or the Jarvis channel", "command", c.Name,
			"dm", c.DM, "channel", c.ChannelID, "parent", c.ParentID, "want", b.ChannelID)
		return priv(wrongPlaceHelp)
	}
	if c.Name == "link" {
		return b.link(ctx, c, priv)
	}
	link, linked, err := b.Store.ByDiscord(ctx, b.HouseholdID, c.UserID)
	if err != nil {
		b.log().Error("discord: link lookup failed", "err", err)
		return priv("Something went wrong, try again.")
	}
	if !linked {
		return priv(unlinkedHelp)
	}
	key := b.convKey(c.UserID, source)
	switch c.Name {
	case "ask":
		msg := strings.TrimSpace(c.Args["message"])
		if msg == "" {
			return priv("Ask me something, for example `/jarvis ask message:what's the weather`.")
		}
		ctx, cancel := context.WithTimeout(ctx, turnTimeout)
		defer cancel()
		reply, cid, err := b.Chat(ctx, link.UserID, b.HouseholdID, link.NodeID, b.conv(key), msg, nil)
		if err != nil {
			return priv("Jarvis could not answer that: " + clip(err.Error(), 300))
		}
		if cid != "" {
			b.setConv(key, cid)
		}
		return Reply{Text: clip(reply, maxReply)}
	case "new":
		b.dropConv(key)
		return priv("Started a fresh conversation.")
	case "node":
		return b.pickNode(ctx, link, c.Args["name"], priv)
	case "voice":
		mode := strings.ToLower(c.Args["mode"])
		if mode != VoiceAuto && mode != VoiceAlways && mode != VoiceNever {
			return priv("Voice replies can be `auto`, `always` or `never`.")
		}
		if _, err := b.Store.SetPrefs(ctx, b.HouseholdID, link.UserID, Prefs{VoiceReplies: &mode}); err != nil {
			return priv("Something went wrong, try again.")
		}
		return priv("Voice replies: " + mode + ".")
	case "notify":
		on := strings.ToLower(c.Args["state"]) == "on"
		if s := strings.ToLower(c.Args["state"]); s != "on" && s != "off" {
			return priv("Use `/jarvis notify state:on` or `off`.")
		}
		if _, err := b.Store.SetPrefs(ctx, b.HouseholdID, link.UserID, Prefs{Proactive: &on}); err != nil {
			return priv("Something went wrong, try again.")
		}
		if on {
			return priv("I'll send your reminders and alarms to your direct messages.")
		}
		return priv("I won't message you on my own.")
	case "join":
		if b.VoiceCh == nil {
			return priv("Live voice is not set up. An admin needs to enter a voice channel ID in the Jarvis app.")
		}
		msg, err := b.VoiceCh.Join(ctx, c.UserID)
		if err != nil {
			return priv(err.Error())
		}
		return Reply{Text: msg}
	case "leave":
		if b.VoiceCh == nil {
			return priv("Live voice is not set up.")
		}
		return Reply{Text: b.VoiceCh.Leave(ctx)}
	case "status":
		pro := "off"
		if link.Proactive {
			pro = "on"
		}
		return priv(fmt.Sprintf("Linked. Node: %s. Voice replies: %s. Reminder messages: %s.", link.NodeID, link.VoiceReplies, pro))
	}
	return priv("I don't know that command.")
}

func (b *Bridge) link(ctx context.Context, c Command, priv func(string) Reply) Reply {
	hh, userID, nodeID, ok, err := b.Codes.Redeem(c.Args["code"], c.UserID)
	switch {
	case errors.Is(err, ErrTooManyGuesses):
		return priv(err.Error() + ".")
	case err != nil:
		return priv("Something went wrong, try again.")
	case !ok || hh != b.HouseholdID:
		return priv("That code is wrong or has expired. Make a new one in the Jarvis app.")
	}
	if err := b.Store.Put(ctx, Link{HouseholdID: hh, DiscordUserID: c.UserID, UserID: userID, NodeID: nodeID}); err != nil {
		b.log().Error("discord: could not save a link", "err", err)
		return priv("Something went wrong, try again.")
	}
	return priv("Linked. You can talk to me here now. Reminder messages are off; turn them on with `/jarvis notify state:on`.")
}

func (b *Bridge) pickNode(ctx context.Context, l Link, want string, priv func(string) Reply) Reply {
	if b.Nodes == nil {
		return priv("Choosing a node is not available.")
	}
	nodes, err := b.Nodes(ctx, b.HouseholdID)
	if err != nil {
		return priv("Something went wrong, try again.")
	}
	want = strings.TrimSpace(want)
	var names []string
	for _, n := range nodes {
		names = append(names, n.Name)
		if strings.EqualFold(n.ID, want) || strings.EqualFold(n.Name, want) {
			if _, err := b.Store.SetPrefs(ctx, b.HouseholdID, l.UserID, Prefs{NodeID: &n.ID}); err != nil {
				return priv("Something went wrong, try again.")
			}
			return priv("I'll answer through " + n.Name + ".")
		}
	}
	return priv("I don't know that node. Yours are: " + strings.Join(names, ", ") + ".")
}

// stripMention removes a leading @Jarvis mention.
func stripMention(s, botID string) string {
	s = strings.TrimSpace(s)
	for _, m := range []string{"<@" + botID + ">", "<@!" + botID + ">"} {
		s = strings.TrimSpace(strings.TrimPrefix(s, m))
	}
	return s
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// Split cuts text into pieces of at most max characters, preferring paragraph, line and word breaks.
func Split(s string, max int) []string {
	var out []string
	for utf8.RuneCountInString(s) > max {
		r := []rune(s)
		cut := max
		window := string(r[:max])
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(window, sep); i > max/2 {
				cut = utf8.RuneCountInString(window[:i])
				break
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), " \n"))
		s = strings.TrimLeft(string(r[cut:]), " \n")
	}
	if s != "" || len(out) == 0 {
		out = append(out, s)
	}
	return out
}

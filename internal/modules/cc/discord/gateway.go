package discord

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/thomas-vilte/dave-go/session"
)

// Config is one household's bot.
type Config struct {
	HouseholdID    string
	Token          string
	GuildID        string
	ChannelID      string
	VoiceChannelID string
}

// same reports whether two configs describe the same bot, so a change restarts it.
func (c Config) same(o Config) bool { return c == o }

// noMentions keeps a reply from pinging anyone, whatever the text says.
var noMentions = &discord.AllowedMentions{}

// Gateway is a connected bot: it turns Discord events into Bridge calls and implements Out.
type Gateway struct {
	cfg    Config
	bridge *Bridge
	client *bot.Client
	log    *slog.Logger

	mu      sync.Mutex
	parents map[snowflake.ID]string // thread id to parent channel id, cached
	vs      *voiceSession
}

// Connect opens the Discord gateway for a household and registers the /jarvis command in its
// guild. Close stops it. bridge.Out and bridge.BotUserID are filled in here.
func Connect(ctx context.Context, cfg Config, bridge *Bridge, log *slog.Logger) (*Gateway, error) {
	g := &Gateway{cfg: cfg, bridge: bridge, log: log, parents: map[snowflake.ID]string{}}
	client, err := disgo.New(cfg.Token,
		bot.WithGatewayConfigOpts(gateway.WithIntents(
			gateway.IntentGuilds, gateway.IntentGuildMessages, gateway.IntentDirectMessages, gateway.IntentMessageContent,
			gateway.IntentGuildVoiceStates)),
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagVoiceStates)),
		bot.WithVoiceManagerConfigOpts(voice.WithDaveSessionCreateFunc(session.CreateFunc())),
		bot.WithEventListenerFunc(g.onMessage),
		bot.WithEventListenerFunc(g.onCommand),
	)
	if err != nil {
		return nil, err
	}
	g.client = client
	bridge.Out = g
	bridge.VoiceCh = g
	bridge.ChannelID, bridge.VoiceChannelID = cfg.ChannelID, cfg.VoiceChannelID // one source of truth: the config
	if err := client.OpenGateway(ctx); err != nil {
		client.Close(context.Background())
		return nil, err
	}
	me, ok := client.Caches.SelfUser()
	for i := 0; !ok && i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		me, ok = client.Caches.SelfUser()
	}
	if !ok {
		client.Close(context.Background())
		return nil, errors.New("discord: the bot did not become ready")
	}
	bridge.BotUserID = me.ID.String()
	if gid, err := snowflake.Parse(cfg.GuildID); err == nil {
		if _, err := client.Rest.SetGuildCommands(client.ApplicationID, gid, slashCommands()); err != nil {
			log.Warn("discord: could not register slash commands", "household", cfg.HouseholdID, "err", err)
		}
	}
	return g, nil
}

// Close disconnects the bot, leaving any voice channel first.
func (g *Gateway) Close() {
	g.Leave(context.Background())
	g.client.Close(context.Background())
}

func slashCommands() []discord.ApplicationCommandCreate {
	str := func(name, desc string, required bool, choices ...string) discord.ApplicationCommandOptionString {
		o := discord.ApplicationCommandOptionString{Name: name, Description: desc, Required: required}
		for _, c := range choices {
			o.Choices = append(o.Choices, discord.ApplicationCommandOptionChoiceString{Name: c, Value: c})
		}
		return o
	}
	sub := func(name, desc string, opts ...discord.ApplicationCommandOption) discord.ApplicationCommandOptionSubCommand {
		return discord.ApplicationCommandOptionSubCommand{Name: name, Description: desc, Options: opts}
	}
	return []discord.ApplicationCommandCreate{discord.SlashCommandCreate{
		Name:        "jarvis",
		Description: "Talk to Jarvis",
		Options: []discord.ApplicationCommandOption{
			sub("ask", "Ask Jarvis something", str("message", "What to ask", true)),
			sub("new", "Start a fresh conversation"),
			sub("link", "Link this Discord account to your Jarvis account", str("code", "The code from the Jarvis app", true)),
			sub("node", "Choose which Jarvis node answers", str("name", "Node name", true)),
			sub("voice", "When Jarvis replies with a voice clip", str("mode", "auto, always or never", true, VoiceAuto, VoiceAlways, VoiceNever)),
			sub("notify", "Reminders and alarms in your direct messages", str("state", "on or off", true, "on", "off")),
			sub("join", "Join the Jarvis voice channel to talk live"),
			sub("leave", "Leave the voice channel"),
			sub("status", "Show your link and settings"),
		},
	}}
}

// --- events ---

func (g *Gateway) onMessage(e *events.MessageCreate) {
	m := e.Message
	var atts []Attachment
	for _, a := range m.Attachments {
		at := Attachment{URL: a.URL, Size: int64(a.Size), VoiceMessage: m.Flags.Has(discord.MessageFlagIsVoiceMessage)}
		if a.ContentType != nil {
			at.ContentType = *a.ContentType
		}
		if a.DurationSecs != nil {
			at.Seconds = *a.DurationSecs
		}
		atts = append(atts, at)
	}
	msg := Message{ID: m.ID.String(), ChannelID: m.ChannelID.String(), AuthorID: m.Author.ID.String(),
		AuthorBot: m.Author.Bot || m.Author.System, DM: m.GuildID == nil, Content: m.Content, Attachments: atts}
	if !msg.DM && msg.ChannelID != g.bridge.ChannelID && msg.ChannelID != g.bridge.VoiceChannelID {
		msg.ParentID = g.parentOf(m.ChannelID)
	}
	go g.bridge.HandleMessage(context.Background(), msg)
}

// parentOf is the parent channel of a thread ("" for anything else), cached.
func (g *Gateway) parentOf(ch snowflake.ID) string {
	g.mu.Lock()
	if p, ok := g.parents[ch]; ok {
		g.mu.Unlock()
		return p
	}
	g.mu.Unlock()
	parent := ""
	if c, err := g.client.Rest.GetChannel(ch); err == nil {
		if t, ok := c.(discord.GuildThread); ok && t.ParentID() != nil {
			parent = t.ParentID().String()
		}
	}
	g.mu.Lock()
	g.parents[ch] = parent
	g.mu.Unlock()
	return parent
}

func (g *Gateway) onCommand(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok || data.CommandName() != "jarvis" || data.SubCommandName == nil {
		return
	}
	c := Command{UserID: e.User().ID.String(), ChannelID: e.Channel().ID().String(), DM: e.GuildID() == nil,
		Name: *data.SubCommandName, Args: map[string]string{}}
	for _, k := range []string{"message", "code", "name", "mode", "state"} {
		if v, ok := data.OptString(k); ok {
			c.Args[k] = v
		}
	}
	if !c.DM && c.ChannelID != g.bridge.ChannelID && c.ChannelID != g.bridge.VoiceChannelID {
		c.ParentID = g.parentOf(e.Channel().ID())
	}
	go func() {
		ctx := context.Background()
		if c.Name == "ask" { // a turn can outlast Discord's three second window
			if err := e.DeferCreateMessage(false); err != nil {
				g.log.Warn("discord: could not defer", "err", err)
				return
			}
			r := g.bridge.HandleCommand(ctx, c)
			_, err := g.client.Rest.UpdateInteractionResponse(g.client.ApplicationID, e.Token(),
				discord.MessageUpdate{Content: &r.Text, AllowedMentions: noMentions})
			if err != nil {
				g.log.Warn("discord: could not answer a command", "err", err)
			}
			return
		}
		r := g.bridge.HandleCommand(ctx, c)
		b := discord.MessageCreate{Content: r.Text, AllowedMentions: noMentions}
		if r.Ephemeral {
			b.Flags = discord.MessageFlagEphemeral
		}
		if err := e.CreateMessage(b); err != nil {
			g.log.Warn("discord: could not answer a command", "err", err)
		}
	}()
}

// --- Out ---

func sf(s string) (snowflake.ID, error) { return snowflake.Parse(s) }

func (g *Gateway) React(_ context.Context, channelID, messageID, emoji string) error {
	ch, err1 := sf(channelID)
	msg, err2 := sf(messageID)
	if err := errors.Join(err1, err2); err != nil {
		return err
	}
	return g.client.Rest.AddReaction(ch, msg, emoji)
}

func (g *Gateway) Typing(_ context.Context, channelID string) error {
	ch, err := sf(channelID)
	if err != nil {
		return err
	}
	return g.client.Rest.SendTyping(ch)
}

func (g *Gateway) Send(_ context.Context, channelID, text string) error {
	ch, err := sf(channelID)
	if err != nil {
		return err
	}
	_, err = g.client.Rest.CreateMessage(ch, discord.MessageCreate{Content: text, AllowedMentions: noMentions})
	return err
}

func (g *Gateway) StartThread(_ context.Context, channelID, messageID, name string) (string, error) {
	ch, err1 := sf(channelID)
	msg, err2 := sf(messageID)
	if err := errors.Join(err1, err2); err != nil {
		return "", err
	}
	t, err := g.client.Rest.CreateThreadFromMessage(ch, msg, discord.ThreadCreateFromMessage{
		Name: name, AutoArchiveDuration: discord.AutoArchiveDuration1h})
	if err != nil {
		return "", err
	}
	return t.ID().String(), nil
}

func (g *Gateway) SendDM(_ context.Context, discordUserID, text string) error {
	u, err := sf(discordUserID)
	if err != nil {
		return err
	}
	dm, err := g.client.Rest.CreateDMChannel(u)
	if err != nil {
		return err
	}
	_, err = g.client.Rest.CreateMessage(dm.ID(), discord.MessageCreate{Content: text, AllowedMentions: noMentions})
	return err
}

// voiceFlag is Discord's IS_VOICE_MESSAGE message flag.
const voiceFlag = 1 << 13

// SendVoice posts an Ogg Opus clip as a Discord voice message. disgo cannot set the duration and
// waveform Discord requires, so this is a direct multipart request.
func (g *Gateway) SendVoice(ctx context.Context, channelID string, ogg []byte, seconds float64) error {
	// A flat waveform: the real shape is cosmetic, and 64 samples is enough for the player bar.
	wave := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{110}, 64))
	payload, _ := json.Marshal(map[string]any{
		"flags":            voiceFlag,
		"attachments":      []map[string]any{{"id": 0, "filename": "voice-message.ogg", "duration_secs": seconds, "waveform": wave}},
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="payload_json"`)
	h.Set("Content-Type", "application/json")
	pw, _ := mw.CreatePart(h)
	pw.Write(payload)
	fh := textproto.MIMEHeader{}
	fh.Set("Content-Disposition", `form-data; name="files[0]"; filename="voice-message.ogg"`)
	fh.Set("Content-Type", "audio/ogg")
	fw, _ := mw.CreatePart(fh)
	fw.Write(ogg)
	mw.Close()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://discord.com/api/v10/channels/"+channelID+"/messages", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bot "+g.cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("discord: voice message: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

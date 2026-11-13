package discord

import (
	"context"
	"errors"
	"time"

	audioclip "github.com/alexberardi/jarvis-server/internal/modules/cc/discord/clip"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/discord/opusnative"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// aloneFor is how long the bot stays in a voice channel nobody else is in.
const aloneFor = 60 * time.Second

type voiceSession struct {
	conn   voice.Conn
	voice  *Voice
	cancel context.CancelFunc
}

// receiver adapts the Discord audio receiver to Voice.
type receiver struct{ v *Voice }

func (r receiver) ReceiveOpusFrame(u snowflake.ID, p *voice.Packet) error {
	r.v.Frame(u.String(), p.Opus)
	return nil
}
func (receiver) CleanupUser(snowflake.ID) {}
func (receiver) Close()                   {}

type libopusDecoder struct{ d *opusnative.Decoder }

func (l libopusDecoder) Decode(p []byte) ([]int16, error) { return l.d.Decode(p) }
func (l libopusDecoder) Close()                           { l.d.Close() }

// Join joins the Jarvis voice channel, only if the person asking is in it, linked, and the bot
// has what it needs. It says so in the text channel, so nobody is recorded unaware.
func (g *Gateway) Join(ctx context.Context, discordUserID string) (string, error) {
	b := g.bridge
	switch {
	case g.cfg.VoiceChannelID == "":
		return "", errors.New("Live voice is not set up. An admin needs to enter a voice channel ID in the Jarvis app.")
	case b.HearWAV == nil || b.Synth == nil:
		return "", errors.New("Live voice needs speech to text and text to speech, which this server does not have.")
	}
	if err := opusnative.Load(); err != nil {
		return "", errors.New("Live voice needs libopus on the server, which is not installed.")
	}
	guild, err1 := snowflake.Parse(g.cfg.GuildID)
	vc, err2 := snowflake.Parse(g.cfg.VoiceChannelID)
	user, err3 := snowflake.Parse(discordUserID)
	if err := errors.Join(err1, err2, err3); err != nil {
		return "", errors.New("The Discord IDs in the Jarvis settings are not valid.")
	}
	st, ok := g.client.Caches.VoiceState(guild, user)
	if !ok || st.ChannelID == nil || *st.ChannelID != vc {
		return "", errors.New("Join the Jarvis voice channel first, then run this again.")
	}
	g.mu.Lock()
	if g.vs != nil {
		g.mu.Unlock()
		return "I'm already in the voice channel.", nil
	}
	g.mu.Unlock()

	conn := g.client.VoiceManager.CreateConn(guild)
	if err := conn.Open(ctx, vc, false, false); err != nil {
		g.log.Warn("discord voice: could not join", "err", err)
		return "", errors.New("I couldn't join the voice channel. Check the bot can connect and speak there.")
	}
	player := &QueuePlayer{}
	v := &Voice{HouseholdID: g.cfg.HouseholdID, Store: b.Store, Chat: b.Chat, Hear: b.HearWAV, Synth: b.Synth,
		WAV: audioclip.PCMToWAV, Player: player, Log: g.log,
		NewDecoder: func() (PCMDecoder, error) {
			d, err := opusnative.NewDecoder()
			if err != nil {
				return nil, err
			}
			return libopusDecoder{d}, nil
		}}
	conn.SetOpusFrameReceiver(receiver{v})
	conn.SetOpusFrameProvider(player)
	vctx, cancel := context.WithCancel(context.Background())
	go v.Run(vctx)
	g.mu.Lock()
	g.vs = &voiceSession{conn: conn, voice: v, cancel: cancel}
	g.mu.Unlock()
	go g.watchAlone(vctx, guild, vc)
	_ = b.Out.Send(ctx, b.ChannelID, "Joined the voice channel. I hear linked people only, keep nothing, and leave when it's empty. `/jarvis leave` sends me away.")
	return "Joined. Talk whenever you like.", nil
}

// watchAlone leaves when nobody but the bot has been in the channel for a minute.
func (g *Gateway) watchAlone(ctx context.Context, guild, vc snowflake.ID) {
	var since time.Time
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		people := 0
		for vs := range g.client.Caches.VoiceStates(guild) {
			if vs.ChannelID != nil && *vs.ChannelID == vc && vs.UserID != g.client.ApplicationID {
				people++
			}
		}
		switch {
		case people > 0:
			since = time.Time{}
		case since.IsZero():
			since = time.Now()
		case time.Since(since) >= aloneFor:
			g.Leave(context.Background())
			return
		}
	}
}

// Leave stops listening and speaking and disconnects. It is safe to call when not connected.
func (g *Gateway) Leave(ctx context.Context) string {
	g.mu.Lock()
	vs := g.vs
	g.vs = nil
	g.mu.Unlock()
	if vs == nil {
		return "I'm not in a voice channel."
	}
	vs.cancel()
	vs.voice.Close()
	vs.conn.Close(ctx)
	_ = g.bridge.Out.Send(ctx, g.bridge.ChannelID, "Left the voice channel.")
	return "Left the voice channel."
}

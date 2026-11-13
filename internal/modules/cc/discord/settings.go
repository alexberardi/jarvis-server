package discord

import (
	"context"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The household's Discord settings. They are entered in the mobile app (household settings, admin
// only) like the Twilio credentials, never in a file or the environment. The token is a secret:
// write only, shown masked.
const (
	SettingEnabled        = "discord.enabled"
	SettingBotToken       = "discord.bot_token"
	SettingGuildID        = "discord.guild_id"
	SettingChannelID      = "discord.channel_id"
	SettingVoiceChannelID = "discord.voice_channel_id"
)

// Definitions declares the settings.
func Definitions() []settings.Definition {
	return []settings.Definition{
		{Key: SettingEnabled, Category: "discord", Type: settings.Bool, Default: false,
			Description: "Answer messages sent to the household's Discord bot. Needs the bot token, server and channel below."},
		{Key: SettingBotToken, Category: "discord", Type: settings.String, Default: "", IsSecret: true,
			Description: "The Discord bot token (Developer Portal, Bot, Reset Token). Write only."},
		{Key: SettingGuildID, Category: "discord", Type: settings.String, Default: "", Validate: snowflakeOrEmpty,
			Description: "The Discord server (guild) ID the bot works in."},
		{Key: SettingChannelID, Category: "discord", Type: settings.String, Default: "", Validate: snowflakeOrEmpty,
			Description: "The one text channel the bot answers in, besides direct messages."},
		{Key: SettingVoiceChannelID, Category: "discord", Type: settings.String, Default: "", Validate: snowflakeOrEmpty,
			Description: "The one voice channel the bot joins for live conversation. Empty: no live voice."},
	}
}

var discordID = regexp.MustCompile(`^[0-9]{15,22}$`)

func snowflakeOrEmpty(v any) error {
	s, _ := v.(string)
	if s = strings.TrimSpace(s); s != "" && !discordID.MatchString(s) {
		return errNotSnowflake
	}
	return nil
}

var errNotSnowflake = errString("expected a Discord ID (15 to 22 digits). Turn on Developer Mode, right-click, Copy ID")

type errString string

func (e errString) Error() string { return string(e) }

// SettingsSource reads each household's bot config from settings.
type SettingsSource struct{ Settings *settings.Service }

// Configs lists the households that have the bridge on and complete. A household missing its
// token, server or channel is left out rather than half started.
func (s SettingsSource) Configs(ctx context.Context) ([]Config, error) {
	on, err := s.Settings.HouseholdValues(ctx, SettingEnabled)
	if err != nil {
		return nil, err
	}
	var out []Config
	for _, hv := range on {
		if b, _ := hv.Value.(bool); !b {
			continue
		}
		get := func(key string) string {
			v, found, err := s.Settings.GetExact(ctx, key, settings.Scope{HouseholdID: hv.HouseholdID})
			if err != nil || !found {
				return ""
			}
			str, _ := v.(string)
			return strings.TrimSpace(str)
		}
		c := Config{HouseholdID: hv.HouseholdID, Token: get(SettingBotToken), GuildID: get(SettingGuildID),
			ChannelID: get(SettingChannelID), VoiceChannelID: get(SettingVoiceChannelID)}
		if c.Token == "" || c.GuildID == "" || c.ChannelID == "" {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

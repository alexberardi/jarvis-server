package discord_test

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/discord"
)

func TestAllowedFetch(t *testing.T) {
	for url, want := range map[string]bool{
		"https://cdn.discordapp.com/attachments/1/2/a.png":   true,
		"https://media.discordapp.net/attachments/1/2/a.png": true,
		"http://cdn.discordapp.com/a.png":                    false,
		"https://cdn.discordapp.com.evil.example/a.png":      false,
		"https://evil.example/cdn.discordapp.com/a.png":      false,
		"https://user@cdn.discordapp.com/a.png":              false,
		"https://169.254.169.254/latest/meta-data":           false,
		"https://localhost/a.png":                            false,
		"file:///etc/passwd":                                 false,
		"":                                                   false,
	} {
		if got := discord.AllowedFetch(url); got != want {
			t.Errorf("AllowedFetch(%q) = %v, want %v", url, got, want)
		}
	}
}

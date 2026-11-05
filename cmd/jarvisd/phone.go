package main

import (
	"log/slog"
	"os"

	"github.com/alexberardi/jarvis-server/internal/platform/config"

	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/simline"
)

// phoneConfig is the telephony bootstrap from the environment (config.Env). Twilio credentials are
// per-household cc settings (AD6: phone.twilio_account_sid, phone.twilio_auth_token,
// phone.twilio_from_number), resolved household → system default → these TWILIO_* values, and
// a Twilio client is built per call from whichever level has them. A household with none
// anywhere gets an honest "phone not configured". JARVIS_PHONE_PUBLIC_URL is the https base a
// tunnel maps to the command-center listener; only phone.MediaPath needs to be exposed.
// Households still have to turn phone_calls.enabled on.
func phoneConfig(env config.Env) ccmod.PhoneConfig {
	cfg := ccmod.PhoneConfig{
		EnvCredentials: phone.Credentials{
			AccountSID: env.TwilioAccountSID,
			AuthToken:  env.TwilioAuthToken,
			FromNumber: env.TwilioFromNumber,
		},
		Options: phone.Options{
			PublicURL:    env.PhonePublicURL,
			PublicWSSURL: env.PhonePublicWSSURL,
		},
	}
	// JARVIS_DEV_PHONE_SIM (dev sandboxes only: scripts/sandbox up --phone-sim) sends every
	// household's calls to a simulated line whose far end is the driver at that URL
	// (tools/pipeline call), instead of Twilio or SIP.
	if url := os.Getenv("JARVIS_DEV_PHONE_SIM"); url != "" {
		slog.Warn("JARVIS_DEV_PHONE_SIM is set: phone calls go to a simulated line; for dev sandboxes only", "driver", url)
		line := simline.New(url, slog.Default())
		cfg.Provider, cfg.STT, cfg.TTS = line, line, line
	}
	return cfg
}

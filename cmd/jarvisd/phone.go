package main

import (
	"github.com/alexberardi/jarvis-server/internal/platform/config"

	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
)

// phoneConfig is the telephony bootstrap from the environment (config.Env). Twilio credentials are
// per-household cc settings (AD6: phone.twilio_account_sid, phone.twilio_auth_token,
// phone.twilio_from_number), resolved household → system default → these TWILIO_* values, and
// a Twilio client is built per call from whichever level has them. A household with none
// anywhere gets an honest "phone not configured". JARVIS_PHONE_PUBLIC_URL is the https base a
// tunnel maps to the command-center listener; only phone.MediaPath needs to be exposed.
// Households still have to turn phone_calls.enabled on.
func phoneConfig(env config.Env) ccmod.PhoneConfig {
	return ccmod.PhoneConfig{
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
}

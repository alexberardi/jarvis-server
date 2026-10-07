package main

import (
	"os"

	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
)

// phoneConfig reads the telephony bootstrap from the environment (secrets stay out of the
// settings DB, as they stayed in the phone gateway's env). Without all three Twilio values no
// provider is configured and a confirmed call fails with an honest card. JARVIS_PHONE_PUBLIC_URL
// is the https base a tunnel maps to the command-center listener; only phone.MediaPath needs
// to be exposed. Households still have to turn phone_calls.enabled on.
func phoneConfig() ccmod.PhoneConfig {
	sid, token, from := os.Getenv("TWILIO_ACCOUNT_SID"), os.Getenv("TWILIO_AUTH_TOKEN"), os.Getenv("TWILIO_FROM_NUMBER")
	c := ccmod.PhoneConfig{Options: phone.Options{
		PublicURL:    os.Getenv("JARVIS_PHONE_PUBLIC_URL"),
		PublicWSSURL: os.Getenv("JARVIS_PHONE_PUBLIC_WSS_URL"),
		AuthToken:    token,
	}}
	if sid != "" && token != "" && from != "" {
		c.Provider = &phone.Twilio{AccountSID: sid, AuthToken: token, FromNumber: from}
	}
	return c
}

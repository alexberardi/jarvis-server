package phone

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Per-household telephony credentials (AD6): each household may bring its own Twilio account
// (multi-tenant installs), the install may set a system default, and the TWILIO_* environment
// is the last fallback. The three values are one account, so they resolve as a set from the
// first level that has any of them; a partial set is an error rather than a silent mix of two
// accounts. The provider and the media-stream signing key are resolved per call from the
// session's household and pinned on the call for its lifetime.

// Setting keys (cc settings, household or system scope). The account SID and auth token are
// secrets: the settings API masks them and nothing logs them.
const (
	SettingTwilioAccountSID = "phone.twilio_account_sid"
	SettingTwilioAuthToken  = "phone.twilio_auth_token"
	SettingTwilioFromNumber = "phone.twilio_from_number"
)

// Env fallbacks, read by main into Service.EnvCredentials.
const (
	EnvTwilioAccountSID = "TWILIO_ACCOUNT_SID"
	EnvTwilioAuthToken  = "TWILIO_AUTH_TOKEN"
	EnvTwilioFromNumber = "TWILIO_FROM_NUMBER"
)

// credentialDefinitions are the Twilio settings, appended to the phone settings.
func credentialDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: SettingTwilioAccountSID, Category: "phone_calls", Type: settings.String, Default: "", IsSecret: true,
			EnvFallback: EnvTwilioAccountSID,
			Description: "Twilio account SID for this household's calls (system scope: the install's default). " +
				"Set together with the auth token and from number."},
		{Key: SettingTwilioAuthToken, Category: "phone_calls", Type: settings.String, Default: "", IsSecret: true,
			EnvFallback: EnvTwilioAuthToken,
			Description: "Twilio auth token; also verifies that media streams come from Twilio. Write-only."},
		{Key: SettingTwilioFromNumber, Category: "phone_calls", Type: settings.String, Default: "",
			EnvFallback: EnvTwilioFromNumber,
			Description: "The Twilio number calls are placed from (E.164), on the same account as the SID."},
	}
}

// Credentials are one telephony account.
type Credentials struct {
	AccountSID string
	AuthToken  string
	FromNumber string
}

func (c Credentials) any() bool { return c.AccountSID != "" || c.AuthToken != "" || c.FromNumber != "" }

// missing names the unset values by setting key (never the values themselves).
func (c Credentials) missing() []string {
	var out []string
	if c.AccountSID == "" {
		out = append(out, SettingTwilioAccountSID)
	}
	if c.AuthToken == "" {
		out = append(out, SettingTwilioAuthToken)
	}
	if c.FromNumber == "" {
		out = append(out, SettingTwilioFromNumber)
	}
	return out
}

// String never prints the secrets (a Credentials that reaches a log by accident stays safe).
func (c Credentials) String() string {
	return fmt.Sprintf("phone.Credentials{from=%s, sid_set=%t, token_set=%t}", c.FromNumber, c.AccountSID != "", c.AuthToken != "")
}

// GoString keeps %#v as safe as %v.
func (c Credentials) GoString() string { return c.String() }

// Credential sources, in resolution order.
const (
	SourceHousehold = "household"
	SourceSystem    = "system"
	SourceEnv       = "env"
	// SourceStatic is the single-provider path (Service.Provider set: tests, a fake).
	SourceStatic = "static"
)

// ErrNotConfigured means no level has any telephony credentials for the household.
var ErrNotConfigured = errors.New("phone not configured: no Twilio credentials for this household, " +
	"no system default and no TWILIO_* environment")

// IncompleteError is a level with some but not all of the three values.
type IncompleteError struct {
	Source  string   // household | system | env
	Missing []string // setting keys
}

func (e *IncompleteError) Error() string {
	return "phone not configured: the " + e.Source + " Twilio credentials are incomplete (missing " +
		strings.Join(e.Missing, ", ") + ")"
}

// Telephony is what one household's calls use: the provider and the key that verifies the
// provider's signed requests to us.
type Telephony struct {
	Provider   Provider
	SigningKey string
	Source     string
}

// isNotConfigured reports a missing or partial account (as opposed to a settings read error).
func isNotConfigured(err error) bool {
	var inc *IncompleteError
	return errors.Is(err, ErrNotConfigured) || errors.As(err, &inc)
}

// NotConfiguredMessage is the user-facing sentence for a telephony resolution error.
func NotConfiguredMessage(err error) string {
	var inc *IncompleteError
	if errors.As(err, &inc) {
		return "Phone calls aren't fully set up for this household: the Twilio account details are " +
			"incomplete. A household admin can finish them in Household Settings."
	}
	return "Phone calls aren't set up for this household yet: there's no Twilio account to call from. " +
		"A household admin can add one in Household Settings."
}

// Telephony resolves hh's provider: Service.Provider when set (one provider for every
// household, signed with Options.AuthToken), else household → system default → env
// credentials. Errors are ErrNotConfigured, *IncompleteError, or a settings read failure.
func (s *Service) Telephony(ctx context.Context, hh string) (Telephony, error) {
	if s.Provider != nil {
		return Telephony{Provider: s.Provider, SigningKey: s.Options.AuthToken, Source: SourceStatic}, nil
	}
	creds, source, err := s.credentials(ctx, hh)
	if err != nil {
		return Telephony{}, err
	}
	build := s.NewProvider
	if build == nil {
		build = func(c Credentials) Provider {
			return &Twilio{AccountSID: c.AccountSID, AuthToken: c.AuthToken, FromNumber: c.FromNumber}
		}
	}
	return Telephony{Provider: build(creds), SigningKey: creds.AuthToken, Source: source}, nil
}

func (s *Service) credentials(ctx context.Context, hh string) (Credentials, string, error) {
	type level struct {
		source string
		read   func() (Credentials, error)
	}
	levels := []level{}
	if s.Settings != nil {
		if hh != "" {
			levels = append(levels, level{SourceHousehold, func() (Credentials, error) {
				return s.settingsCredentials(ctx, settings.Scope{HouseholdID: hh})
			}})
		}
		levels = append(levels, level{SourceSystem, func() (Credentials, error) {
			return s.settingsCredentials(ctx, settings.Scope{})
		}})
	}
	levels = append(levels, level{SourceEnv, func() (Credentials, error) { return s.EnvCredentials, nil }})
	for _, l := range levels {
		c, err := l.read()
		if err != nil {
			return Credentials{}, "", err
		}
		if !c.any() {
			continue
		}
		if m := c.missing(); len(m) > 0 {
			return Credentials{}, "", &IncompleteError{Source: l.source, Missing: m}
		}
		return c, l.source, nil
	}
	return Credentials{}, "", ErrNotConfigured
}

func (s *Service) settingsCredentials(ctx context.Context, sc settings.Scope) (Credentials, error) {
	var c Credentials
	for _, f := range []struct {
		key string
		dst *string
	}{{SettingTwilioAccountSID, &c.AccountSID}, {SettingTwilioAuthToken, &c.AuthToken}, {SettingTwilioFromNumber, &c.FromNumber}} {
		v, ok, err := s.Settings.GetExact(ctx, f.key, sc)
		if errors.Is(err, settings.ErrUnknownKey) {
			continue // a settings service without the phone definitions (tests)
		}
		if err != nil {
			return Credentials{}, fmt.Errorf("phone: reading %s: %w", f.key, err)
		}
		if str, isStr := v.(string); ok && isStr {
			*f.dst = strings.TrimSpace(str)
		}
	}
	return c, nil
}

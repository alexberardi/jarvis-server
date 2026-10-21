package phone

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Per-household telephony (AD6): phone.provider picks the household's provider (household →
// system → default twilio; only twilio exists so far, SIP is docs/cc/phone-sip.md step 2), and
// that provider's credentials resolve the same way. Each household may bring its own Twilio
// account (multi-tenant installs), the install may set a system default, and the TWILIO_*
// environment is the last fallback. The three values are one account, so they resolve as a
// set from the first level that has any of them; a partial set is an error rather than a
// silent mix of two accounts. The provider (with its media-stream signing key) is resolved per
// call from the session's household and pinned on the call for its lifetime.

// SettingProvider is the household's telephony provider (household or system scope).
const SettingProvider = "phone.provider"

// Providers (phone.provider values).
const (
	ProviderTwilio = "twilio"
)

// providers are phone.provider's options.
var providers = []any{ProviderTwilio}

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

// credentialDefinitions are the provider choice and the Twilio settings, appended to the
// phone settings.
func credentialDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: SettingProvider, Category: "phone_calls", Type: settings.String, Default: ProviderTwilio,
			Options: providers, Validate: validProvider,
			Description: "Which telephony provider places this household's calls (system scope: the install's " +
				"default). twilio: a Twilio account (the phone.twilio_* settings) and a public URL for its media stream."},
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

func validProvider(v any) error {
	str, ok := v.(string)
	if !ok || !slices.Contains(providers, any(normalizeProvider(str))) {
		return fmt.Errorf("unknown phone provider %q (one of: %s)", str, ProviderTwilio)
	}
	return nil
}

func normalizeProvider(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ProviderTwilio
	}
	return v
}

// ErrNotConfigured means no level has any credentials for the household's provider.
var ErrNotConfigured = errors.New("phone not configured: no Twilio credentials for this household, " +
	"no system default and no TWILIO_* environment")

// UnknownProviderError is a phone.provider value this build has no provider for (a value
// written before a downgrade, or straight into the table).
type UnknownProviderError struct{ Provider string }

func (e *UnknownProviderError) Error() string {
	return fmt.Sprintf("phone not configured: unknown phone provider %q", e.Provider)
}

// IncompleteError is a level with some but not all of the three values.
type IncompleteError struct {
	Source  string   // household | system | env
	Missing []string // setting keys
}

func (e *IncompleteError) Error() string {
	return "phone not configured: the " + e.Source + " Twilio credentials are incomplete (missing " +
		strings.Join(e.Missing, ", ") + ")"
}

// Telephony is what one household's calls use: the provider, and the key that verifies the
// provider's signed requests to us (Twilio's auth token; empty for a provider that signs none).
type Telephony struct {
	Provider   Provider
	SigningKey string
	Source     string
}

// isNotConfigured reports a missing or partial account or an unknown provider (as opposed to
// a settings read error).
func isNotConfigured(err error) bool {
	var inc *IncompleteError
	var unk *UnknownProviderError
	return errors.Is(err, ErrNotConfigured) || errors.As(err, &inc) || errors.As(err, &unk)
}

// NotConfiguredMessage is the user-facing sentence for a telephony resolution error.
func NotConfiguredMessage(err error) string {
	var inc *IncompleteError
	var unk *UnknownProviderError
	if errors.As(err, &unk) {
		return "Phone calls aren't set up for this household: its phone provider isn't one this " +
			"version of Jarvis supports. A household admin can choose another in Household Settings."
	}
	if errors.As(err, &inc) {
		return "Phone calls aren't fully set up for this household: the Twilio account details are " +
			"incomplete. A household admin can finish them in Household Settings."
	}
	return "Phone calls aren't set up for this household yet: there's no Twilio account to call from. " +
		"A household admin can add one in Household Settings."
}

// Telephony resolves hh's provider: Service.Provider or Service.Twilio when set (one provider
// for every household; Twilio's signed with Options.AuthToken), else hh's phone.provider with
// its credentials from household → system default → env. Errors are ErrNotConfigured,
// *IncompleteError, *UnknownProviderError, or a settings read failure.
func (s *Service) Telephony(ctx context.Context, hh string) (Telephony, error) {
	if s.Provider != nil {
		return Telephony{Provider: s.Provider, Source: SourceStatic}, nil
	}
	if s.Twilio != nil {
		return Telephony{Provider: s.twilioProvider(s.Twilio, s.Options.AuthToken),
			SigningKey: s.Options.AuthToken, Source: SourceStatic}, nil
	}
	kind, err := s.providerKind(ctx, hh)
	if err != nil {
		return Telephony{}, err
	}
	if kind != ProviderTwilio {
		return Telephony{}, &UnknownProviderError{Provider: kind}
	}
	creds, source, err := s.credentials(ctx, hh)
	if err != nil {
		return Telephony{}, err
	}
	build := s.NewTwilio
	if build == nil {
		build = func(c Credentials) TwilioAPI {
			return &Twilio{AccountSID: c.AccountSID, AuthToken: c.AuthToken, FromNumber: c.FromNumber}
		}
	}
	return Telephony{Provider: s.twilioProvider(build(creds), creds.AuthToken), SigningKey: creds.AuthToken,
		Source: source}, nil
}

// twilioProvider is the Twilio provider over api, verifying its streams with signingKey.
func (s *Service) twilioProvider(api TwilioAPI, signingKey string) *twilioProvider {
	s.init()
	return &twilioProvider{api: api, signingKey: signingKey, media: s.twilio}
}

// providerKind is hh's phone.provider (household → system → twilio). A settings service
// without the phone definitions (tests) is twilio.
func (s *Service) providerKind(ctx context.Context, hh string) (string, error) {
	if s.Settings == nil {
		return ProviderTwilio, nil
	}
	v, err := s.Settings.Get(ctx, SettingProvider, settings.Scope{HouseholdID: hh})
	if errors.Is(err, settings.ErrUnknownKey) {
		return ProviderTwilio, nil
	}
	if err != nil {
		return "", fmt.Errorf("phone: reading %s: %w", SettingProvider, err)
	}
	str, _ := v.Value.(string)
	return normalizeProvider(str), nil
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

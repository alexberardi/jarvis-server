package phone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// AD6: per-household Twilio credentials, resolved household → system default → env, with the
// provider and the media-stream signing key pinned per call.

// accounts is a NewProvider that hands out one fake per account SID and records what it saw.
type accounts struct {
	mu    sync.Mutex
	fakes map[string]*fakeProvider
	built []Credentials
}

func newAccounts() *accounts { return &accounts{fakes: map[string]*fakeProvider{}} }

func (a *accounts) build(c Credentials) Provider {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.built = append(a.built, c)
	f, ok := a.fakes[c.AccountSID]
	if !ok {
		f = newFakeProvider()
		a.fakes[c.AccountSID] = f
	}
	return f
}

func (a *accounts) fake(sid string) *fakeProvider {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.fakes[sid]
	if !ok {
		f = newFakeProvider()
		a.fakes[sid] = f
	}
	return f
}

// perHousehold switches the env from the single fake provider to per-household resolution.
func (e *env) perHousehold() *accounts {
	a := newAccounts()
	e.s.Provider = nil
	e.s.Options.AuthToken = ""
	e.s.NewProvider = a.build
	return a
}

func (e *env) setCreds(sc settings.Scope, sid, token, from string) {
	e.t.Helper()
	e.set(SettingTwilioAccountSID, sid, sc)
	e.set(SettingTwilioAuthToken, token, sc)
	e.set(SettingTwilioFromNumber, from, sc)
}

func TestTelephonyResolutionOrder(t *testing.T) {
	e := newEnv(t)
	a := e.perHousehold()
	ctx := context.Background()

	// Nothing anywhere: a clear, typed "not configured".
	if _, err := e.s.Telephony(ctx, hh); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nothing configured: %v", err)
	}

	// Env only.
	e.s.EnvCredentials = Credentials{AccountSID: "AC-env", AuthToken: "token-env", FromNumber: "+15550000000"}
	tel, err := e.s.Telephony(ctx, hh)
	if err != nil || tel.Source != SourceEnv || tel.SigningKey != "token-env" || tel.Provider != a.fake("AC-env") {
		t.Fatalf("env: %+v %v", tel, err)
	}

	// A system default beats env.
	e.setCreds(settings.Scope{}, "AC-sys", "token-sys", "+15550000009")
	if tel, err = e.s.Telephony(ctx, hh); err != nil || tel.Source != SourceSystem || tel.SigningKey != "token-sys" {
		t.Fatalf("system: %+v %v", tel, err)
	}

	// The household's own account beats both, for that household only.
	e.setCreds(settings.Scope{HouseholdID: hh}, "AC-A", "token-A", "+15550000001")
	if tel, err = e.s.Telephony(ctx, hh); err != nil || tel.Source != SourceHousehold || tel.SigningKey != "token-A" ||
		tel.Provider != a.fake("AC-A") {
		t.Fatalf("household: %+v %v", tel, err)
	}
	if tel, err = e.s.Telephony(ctx, otherHH); err != nil || tel.Source != SourceSystem || tel.SigningKey != "token-sys" {
		t.Fatalf("other household falls back to the system default: %+v %v", tel, err)
	}
	a.mu.Lock()
	last := a.built[len(a.built)-1]
	a.mu.Unlock()
	if last != (Credentials{AccountSID: "AC-sys", AuthToken: "token-sys", FromNumber: "+15550000009"}) {
		t.Fatalf("built from %v", last)
	}

	// Clearing the household's values falls back again.
	e.setCreds(settings.Scope{HouseholdID: hh}, "", "", "")
	if tel, err = e.s.Telephony(ctx, hh); err != nil || tel.Source != SourceSystem {
		t.Fatalf("cleared household: %+v %v", tel, err)
	}

	// A partial household set is an error, never a silent mix with the system account.
	e.set(SettingTwilioAccountSID, "AC-A", settings.Scope{HouseholdID: hh})
	_, err = e.s.Telephony(ctx, hh)
	var inc *IncompleteError
	if !errors.As(err, &inc) || inc.Source != SourceHousehold ||
		strings.Join(inc.Missing, ",") != SettingTwilioAuthToken+","+SettingTwilioFromNumber {
		t.Fatalf("partial: %v", err)
	}
	if strings.Contains(err.Error(), "AC-A") {
		t.Fatalf("error leaks a value: %v", err)
	}
	if !strings.Contains(NotConfiguredMessage(err), "incomplete") {
		t.Fatalf("message: %s", NotConfiguredMessage(err))
	}

	// A partial env set with nothing in settings is also an error.
	e2 := newEnv(t)
	e2.perHousehold()
	e2.s.EnvCredentials = Credentials{AccountSID: "AC-env"}
	if _, err := e2.s.Telephony(ctx, hh); !errors.As(err, &inc) || inc.Source != SourceEnv {
		t.Fatalf("partial env: %v", err)
	}
}

func TestCredentialsNeverFormatSecrets(t *testing.T) {
	c := Credentials{AccountSID: "AC-secret-sid", AuthToken: "secret-token", FromNumber: "+15550000001"}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v %+v %#v %s", c, c, c, c)} {
		if strings.Contains(s, "secret") {
			t.Fatalf("formatted credentials leak: %s", s)
		}
	}
}

func TestNotConfiguredIsClear(t *testing.T) {
	e := newEnv(t)
	e.perHousehold()
	e.enable()
	ctx := context.Background()

	// The tool refuses up front.
	r0 := exec(t, e.s.Tool(), map[string]any{"business": "Tony's", "goal": "order"},
		servertools.Turn{ConversationID: "c1", HouseholdID: hh, Speaker: servertools.Speaker{UserID: 1}})
	if get(r0, "error") != "phone_not_configured" || !strings.Contains(get(r0, "message").(string), "no Twilio account") {
		t.Fatalf("tool: %v", r0)
	}

	// A plan made before the credentials went away: the confirm tap says why and the plan
	// stays a draft, so it can still be confirmed once an admin adds them.
	id := e.draft()
	r := e.s.ConfirmCall(ctx, CallbackContext{HouseholdID: hh, UserID: 2, Data: confirmData(id, "732-592-4183")})
	if r.Success || !strings.HasPrefix(r.Error, "Phone calls aren't set up for this household yet") {
		t.Fatalf("confirm: %+v", r)
	}
	if s := e.session(id); s.State != StateDraft {
		t.Fatalf("state: %s", s.State)
	}
}

// signedDial connects to the media WebSocket the way Twilio does, signing with key.
func signedDial(e *env, wss, key string) (*http.Response, error) {
	sim := &twilioSim{t: e.t, e: e}
	conn, resp, err := sim.dial(wss, live.ComputeSignature(key, wss, nil))
	if conn != nil {
		conn.Close()
	}
	return resp, err
}

func nextDial(t *testing.T, f *fakeProvider) string {
	t.Helper()
	select {
	case call := <-f.started:
		return streamURLRE.FindStringSubmatch(call.twiml)[1]
	case <-time.After(5 * time.Second):
		t.Fatal("no dial")
	}
	return ""
}

func TestSignatureUsesTheSessionsHousehold(t *testing.T) {
	e := newEnv(t)
	a := e.perHousehold()
	e.s.StreamStartTimeout = 500 * time.Millisecond
	var logs bytes.Buffer
	e.s.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx := context.Background()
	e.setCreds(settings.Scope{}, "AC-sys", "token-sys", "+15550000009")
	e.setCreds(settings.Scope{HouseholdID: hh}, "AC-A", "token-A", "+15550000001")
	e.setCreds(settings.Scope{HouseholdID: otherHH}, "AC-B", "token-B", "+15550000002")

	// Household A's call is placed on A's account.
	id := e.confirmed(t)
	wss := nextDial(t, a.fake("AC-A"))
	// A stream signed with household B's token (or the system default's) is refused for A's
	// session; the single-use token is spent by the first attempt.
	if resp, err := signedDial(e, wss, "token-B"); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("B's signature accepted for A's session: %v", err)
	}
	e.waitState(t, id, StateFailed)
	e.s.Wait()
	if got := a.fake("AC-A").endedCalls(); len(got) != 1 || got[0] != "CA1" {
		t.Fatalf("hung up on A's account: %v", got)
	}

	id2 := e.draftAgain()
	if r := e.s.ConfirmCall(ctx, CallbackContext{HouseholdID: hh, UserID: 2, Data: confirmData(id2, "732-592-4183")}); !r.Success {
		t.Fatalf("confirm 2: %+v", r)
	}
	wss = nextDial(t, a.fake("AC-A"))
	if resp, err := signedDial(e, wss, "token-sys"); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("system signature accepted for A's session: %v", err)
	}
	e.waitState(t, id2, StateFailed)
	e.s.Wait()

	// A's own token is accepted.
	id3 := e.draftAgain()
	if r := e.s.ConfirmCall(ctx, CallbackContext{HouseholdID: hh, UserID: 2, Data: confirmData(id3, "732-592-4183")}); !r.Success {
		t.Fatalf("confirm 3: %+v", r)
	}
	wss = nextDial(t, a.fake("AC-A"))
	if _, err := signedDial(e, wss, "token-A"); err != nil {
		t.Fatalf("A's own signature refused: %v", err)
	}
	e.waitState(t, id3, StateFailed) // the fake hung up without a start event
	e.s.Wait()

	// Nothing went to B's or the system account.
	if len(a.fake("AC-B").started) != 0 || len(a.fake("AC-sys").started) != 0 {
		t.Fatal("a call was placed on another account")
	}
	for _, secret := range []string{"token-A", "token-B", "token-sys", "AC-A", "AC-B", "AC-sys"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log contains %q:\n%s", secret, logs.String())
		}
	}
}

func TestEnvFallbackPlacesTheCall(t *testing.T) {
	e := newEnv(t)
	a := e.perHousehold()
	e.s.StreamStartTimeout = 500 * time.Millisecond
	e.s.EnvCredentials = Credentials{AccountSID: "AC-env", AuthToken: "token-env", FromNumber: "+15550000000"}
	id := e.confirmed(t)
	wss := nextDial(t, a.fake("AC-env"))
	if resp, err := signedDial(e, wss, "token-other"); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong signature accepted: %v", err)
	}
	e.waitState(t, id, StateFailed)
	e.s.Wait()

	id2 := e.draftAgain()
	if r := e.s.ConfirmCall(context.Background(), CallbackContext{HouseholdID: hh, UserID: 2, Data: confirmData(id2, "732-592-4183")}); !r.Success {
		t.Fatalf("confirm: %+v", r)
	}
	wss = nextDial(t, a.fake("AC-env"))
	if _, err := signedDial(e, wss, "token-env"); err != nil {
		t.Fatalf("env signature refused: %v", err)
	}
	e.waitState(t, id2, StateFailed)
	e.s.Wait()
}

func TestTwilioSettingsAreMasked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.setCreds(settings.Scope{HouseholdID: hh}, "AC-A", "token-A", "+15550000001")
	e.setCreds(settings.Scope{}, "AC-sys", "token-sys", "+15550000009")
	for _, sc := range []settings.Scope{{}, {HouseholdID: hh}} {
		list, err := e.s.Settings.List(ctx, sc, "phone_calls")
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for _, r := range list {
			switch r.Key {
			case SettingTwilioAccountSID, SettingTwilioAuthToken:
				seen++
				if r.Value != "********" || !r.IsSecret {
					t.Fatalf("%s at %+v not masked: %v", r.Key, sc, r.Value)
				}
			case SettingTwilioFromNumber:
				seen++
			}
		}
		if seen != 3 {
			t.Fatalf("twilio settings listed: %d", seen)
		}
	}
}

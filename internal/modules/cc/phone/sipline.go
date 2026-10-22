package phone

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The SIP provider (docs/cc/phone-sip.md §1): jarvisd registers to the household's PBX as an
// ordinary extension and places calls through the PBX's own routes. A SIP account is one
// credential set resolved like AD6's Twilio set (household → system default, complete or an
// error; no environment fallback): server, extension and password are the set, and the
// account's other values (port, transport, local port, dial format) are read from the same
// level. Each configured account is one long-lived UA (package sip) on its own local port,
// started with cc, kept in step with the settings, and unregistered on shutdown.

// SIP setting keys (cc settings, household or system scope; not in the mobile household
// settings yet). The password is a secret: write-only, masked by the settings API.
const (
	SettingSIPServer     = "phone.sip.server"
	SettingSIPPort       = "phone.sip.port"
	SettingSIPTransport  = "phone.sip.transport"
	SettingSIPExtension  = "phone.sip.extension"
	SettingSIPPassword   = "phone.sip.password"
	SettingSIPLocalPort  = "phone.sip.local_port"
	SettingSIPDialFormat = "phone.sip.dial_format"
	SettingSIPDialPrefix = "phone.sip.dial_prefix"
)

// Dial formats (phone.sip.dial_format): how a NANP number is written for the PBX's outbound
// routes, whose dial patterns are site-specific (NXXNXXXXXX, 1NXXNXXXXXX, +1…).
const (
	DialFormat10   = "10-digit"
	DialFormat11   = "11-digit"
	DialFormatE164 = "e164"
)

// sipReconcileEvery is how often the running UAs are matched to the settings.
const sipReconcileEvery = time.Minute

func sipDefinitions() []settings.Definition {
	port := func(v any) error {
		n, ok := v.(int64)
		if !ok || n < 1 || n > 65535 {
			return fmt.Errorf("a port is 1-65535")
		}
		return nil
	}
	return []settings.Definition{
		{Key: SettingSIPServer, Category: "phone_calls", Type: settings.String, Default: "",
			Description: "SIP: the PBX's address (host name or IP) jarvisd registers to as an extension. " +
				"Set together with the extension and its password (phone.provider = sip)."},
		{Key: SettingSIPPort, Category: "phone_calls", Type: settings.Int, Default: int64(sip.DefaultPort), Validate: port,
			Description: "SIP: the PBX's SIP port."},
		{Key: SettingSIPTransport, Category: "phone_calls", Type: settings.String, Default: "udp",
			Options: []any{"udp", "tcp"}, Validate: oneOf("udp", "tcp"), Description: "SIP: transport to the PBX (udp or tcp)."},
		{Key: SettingSIPExtension, Category: "phone_calls", Type: settings.String, Default: "",
			Description: "SIP: the extension jarvisd registers as (also its authentication user)."},
		{Key: SettingSIPPassword, Category: "phone_calls", Type: settings.String, Default: "", IsSecret: true,
			Description: "SIP: the extension's password (its secret on the PBX). Write-only. After 3 refusals " +
				"jarvisd stops trying, so the PBX's firewall does not ban this host."},
		{Key: SettingSIPLocalPort, Category: "phone_calls", Type: settings.Int, Default: int64(sip.DefaultLocalPort), Validate: port,
			Description: "SIP: jarvisd's own SIP port for this account. It stays the same across restarts so the " +
				"PBX keeps one contact for the extension; each account needs its own."},
		{Key: SettingSIPDialFormat, Category: "phone_calls", Type: settings.String, Default: DialFormat10,
			Options: []any{DialFormat10, DialFormat11, DialFormatE164}, Validate: oneOf(DialFormat10, DialFormat11, DialFormatE164),
			Description: "SIP: how numbers are dialed through the PBX's outbound routes: 10-digit (2125550100), " +
				"11-digit (12125550100) or e164 (+12125550100). US/Canada numbers only."},
		{Key: SettingSIPDialPrefix, Category: "phone_calls", Type: settings.String, Default: "", Validate: validDialPrefix,
			Description: "SIP: digits dialed before every number, for PBXs whose outbound route needs one (e.g. 9)."},
	}
}

func oneOf(opts ...string) func(any) error {
	return func(v any) error {
		if str, ok := v.(string); ok && slices.Contains(opts, strings.ToLower(strings.TrimSpace(str))) {
			return nil
		}
		return fmt.Errorf("one of: %s", strings.Join(opts, ", "))
	}
}

func validDialPrefix(v any) error {
	str, ok := v.(string)
	if !ok {
		return errors.New("a dial prefix is digits")
	}
	str = strings.TrimSpace(str)
	if len(str) > 6 || strings.Trim(str, "0123456789") != "" {
		return errors.New("a dial prefix is up to 6 digits")
	}
	return nil
}

// FormatDial writes a NANP number (any form NormalizeUS takes; it refuses emergency, short
// and premium numbers) in the PBX's dial format, after the prefix.
func FormatDial(number, format, prefix string) (string, error) {
	e164, err := NormalizeUS(number)
	if err != nil {
		return "", err
	}
	ten := strings.TrimPrefix(e164, "+1")
	if err := validDialPrefix(prefix); err != nil {
		return "", err
	}
	prefix = strings.TrimSpace(prefix)
	switch format {
	case "", DialFormat10:
		return prefix + ten, nil
	case DialFormat11:
		return prefix + "1" + ten, nil
	case DialFormatE164:
		return prefix + "+1" + ten, nil
	}
	return "", fmt.Errorf("unknown dial format %q (%s, %s or %s)", format, DialFormat10, DialFormat11, DialFormatE164)
}

// SIPAccount is one resolved SIP account.
type SIPAccount struct {
	Server     string
	Port       int
	Transport  string
	Extension  string
	Password   string
	LocalPort  int
	DialFormat string
	DialPrefix string
}

func (a SIPAccount) any() bool { return a.Server != "" || a.Extension != "" || a.Password != "" }

func (a SIPAccount) missing() []string {
	var out []string
	if a.Server == "" {
		out = append(out, SettingSIPServer)
	}
	if a.Extension == "" {
		out = append(out, SettingSIPExtension)
	}
	if a.Password == "" {
		out = append(out, SettingSIPPassword)
	}
	return out
}

// String never prints the password.
func (a SIPAccount) String() string {
	return fmt.Sprintf("phone.SIPAccount{%s@%s:%d/%s, local port %d, dial %s%s, password_set=%t}",
		a.Extension, a.Server, a.Port, a.Transport, a.LocalPort, a.DialPrefix, a.DialFormat, a.Password != "")
}

// GoString keeps %#v as safe as %v.
func (a SIPAccount) GoString() string { return a.String() }

func (a SIPAccount) config() sip.Config {
	return sip.Config{Server: a.Server, Port: a.Port, Transport: a.Transport, Extension: a.Extension,
		Password: a.Password, LocalPort: a.LocalPort, UserAgent: "Jarvis"}
}

// ErrSIPNotConfigured means the household's provider is sip and no level has a SIP account.
var ErrSIPNotConfigured = errors.New("phone not configured: no SIP account for this household and no system default")

// sipAccount resolves hh's SIP account: household → system, the first level with any of the
// set's three values, which must then have all three.
func (s *Service) sipAccount(ctx context.Context, hh string) (SIPAccount, string, error) {
	if s.Settings == nil {
		return SIPAccount{}, "", ErrSIPNotConfigured
	}
	levels := []struct {
		source string
		sc     settings.Scope
	}{{SourceSystem, settings.Scope{}}}
	if hh != "" {
		levels = slices.Insert(levels, 0, struct {
			source string
			sc     settings.Scope
		}{SourceHousehold, settings.Scope{HouseholdID: hh}})
	}
	for _, l := range levels {
		a, err := s.readSIPAccount(ctx, l.sc)
		if err != nil {
			return SIPAccount{}, "", err
		}
		if !a.any() {
			continue
		}
		if m := a.missing(); len(m) > 0 {
			return SIPAccount{}, "", &IncompleteError{Source: l.source, Missing: m, Provider: ProviderSIP}
		}
		return a, l.source, nil
	}
	return SIPAccount{}, "", ErrSIPNotConfigured
}

// readSIPAccount reads one level's values exactly (no cascade), with the defaults for the
// optional ones.
func (s *Service) readSIPAccount(ctx context.Context, sc settings.Scope) (SIPAccount, error) {
	a := SIPAccount{Port: sip.DefaultPort, Transport: "udp", LocalPort: sip.DefaultLocalPort, DialFormat: DialFormat10}
	str := func(key string, dst *string) error {
		v, ok, err := s.Settings.GetExact(ctx, key, sc)
		if errors.Is(err, settings.ErrUnknownKey) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("phone: reading %s: %w", key, err)
		}
		if x, isStr := v.(string); ok && isStr && strings.TrimSpace(x) != "" {
			*dst = strings.TrimSpace(x)
		}
		return nil
	}
	num := func(key string, dst *int) error {
		v, ok, err := s.Settings.GetExact(ctx, key, sc)
		if errors.Is(err, settings.ErrUnknownKey) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("phone: reading %s: %w", key, err)
		}
		if n, isInt := v.(int64); ok && isInt && n > 0 {
			*dst = int(n)
		}
		return nil
	}
	for _, f := range []func() error{
		func() error { return str(SettingSIPServer, &a.Server) },
		func() error { return str(SettingSIPExtension, &a.Extension) },
		func() error { return str(SettingSIPPassword, &a.Password) },
		func() error { return str(SettingSIPTransport, &a.Transport) },
		func() error { return str(SettingSIPDialFormat, &a.DialFormat) },
		func() error { return str(SettingSIPDialPrefix, &a.DialPrefix) },
		func() error { return num(SettingSIPPort, &a.Port) },
		func() error { return num(SettingSIPLocalPort, &a.LocalPort) },
	} {
		if err := f(); err != nil {
			return SIPAccount{}, err
		}
	}
	a.Transport = strings.ToLower(a.Transport)
	a.DialFormat = strings.ToLower(a.DialFormat)
	return a, nil
}

// sipUA is what the provider needs of a UA (package sip's, or a fake in tests).
type sipUA interface {
	Ready() error
	Dial(ctx context.Context, number string) (sipCall, error)
	// DialFeature calls a self-test feature code (*43, *65): never reachable from Dial.
	DialFeature(ctx context.Context, code string) (sipCall, error)
	// Check re-registers now and pings the registrar (the connection self-test).
	Check(ctx context.Context) sip.CheckResult
	Status() sip.Status
	Close(ctx context.Context)
}

// sipCall is what the Call adapter needs of a SIP call (*sip.Call, or a fake).
type sipCall interface {
	ID() string
	Started() <-chan struct{}
	Frames() <-chan []int16
	Play(pcm []int16) (int64, error)
	Played() (int64, <-chan struct{})
	Flush()
	SendDTMF(digits string) error
	Done() <-chan struct{}
	End() (sip.End, string)
	Hangup(ctx context.Context) error
	Close()
}

// realUA is package sip's UA as a sipUA.
type realUA struct{ *sip.UA }

func (u realUA) Dial(ctx context.Context, number string) (sipCall, error) {
	c, err := u.UA.Dial(ctx, number)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (u realUA) DialFeature(ctx context.Context, code string) (sipCall, error) {
	c, err := u.UA.DialFeature(ctx, code)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func startRealUA(ctx context.Context, a SIPAccount, log func(sip.Config) sip.Config) (sipUA, error) {
	cfg := a.config()
	if log != nil {
		cfg = log(cfg)
	}
	u, err := sip.Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return realUA{u}, nil
}

// sipLines are the running UAs, one per local port.
type sipLines struct {
	reconcile sync.Mutex // one reconcile at a time: two would both start a port's UA
	mu        sync.Mutex
	lines     map[int]*sipLine
}

type sipLine struct {
	acct  SIPAccount
	owner string // the household whose account it is; "" for the system's
	ua    sipUA  // nil when it could not start
	err   error  // why it could not start, or why the account was not used (port clash)
}

// sipProvider is Provider over one SIP account's UA.
type sipProvider struct {
	acct SIPAccount
	line *sipLine
}

// Ready is the UA's registration: a call goes out only through a registered extension.
func (p *sipProvider) Ready() error {
	if p.line.ua == nil {
		return fmt.Errorf("the SIP account could not start: %v", p.line.err)
	}
	return p.line.ua.Ready()
}

func (p *sipProvider) Dial(ctx context.Context, req DialRequest) (Call, error) {
	if p.line.ua == nil {
		return nil, p.Ready()
	}
	number, err := FormatDial(req.To, p.acct.DialFormat, p.acct.DialPrefix)
	if err != nil {
		return nil, err
	}
	c, err := p.line.ua.Dial(ctx, number)
	if err != nil {
		return nil, err
	}
	return &sipCallAdapter{c: c}, nil
}

// LineType has no SIP equivalent (docs/cc/phone-sip.md §1): the plan handles unknown.
func (p *sipProvider) LineType(context.Context, string) string { return "unknown" }

// sipTelephony is hh's SIP provider: its account, and the UA running it (started now if the
// settings changed since the last reconcile).
func (s *Service) sipTelephony(ctx context.Context, hh string) (Telephony, error) {
	acct, source, err := s.sipAccount(ctx, hh)
	if err != nil {
		return Telephony{}, err
	}
	line := s.sipLine(acct)
	if line == nil {
		s.reconcileSIP(ctx)
		line = s.sipLine(acct)
	}
	if line == nil {
		line = &sipLine{acct: acct, err: errors.New("its local port is taken by another household's SIP account " +
			"(give each account its own phone.sip.local_port)")}
	}
	return Telephony{Provider: &sipProvider{acct: acct, line: line}, Source: source}, nil
}

func (s *Service) sipLine(a SIPAccount) *sipLine {
	s.init()
	s.sip.mu.Lock()
	defer s.sip.mu.Unlock()
	if l := s.sip.lines[a.LocalPort]; l != nil && l.acct == a {
		return l
	}
	return nil
}

// SIPStatus is the registration of each running SIP account, by local port (for status pages
// and tests). An account that could not start has Status.State "" and Err set.
type SIPStatus struct {
	Account SIPAccount
	Status  sip.Status
	Err     error
}

// SIPStatuses lists the running SIP accounts.
func (s *Service) SIPStatuses() []SIPStatus {
	s.init()
	s.sip.mu.Lock()
	defer s.sip.mu.Unlock()
	var out []SIPStatus
	for _, l := range s.sip.lines {
		st := SIPStatus{Account: l.acct, Err: l.err}
		if l.ua != nil {
			st.Status = l.ua.Status()
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account.LocalPort < out[j].Account.LocalPort })
	return out
}

// wantedSIP is every SIP account the settings call for: the system's when the install's
// provider is sip, and each household's whose provider is sip. Two accounts on one local port:
// the first (system, then by household id) gets it.
func (s *Service) wantedSIP(ctx context.Context) (map[int]SIPAccount, map[int]string) {
	out, owners := map[int]SIPAccount{}, map[int]string{}
	if s.Settings == nil {
		return out, owners
	}
	add := func(a SIPAccount, owner string) {
		if cur, ok := out[a.LocalPort]; ok && cur != a {
			s.log().Error("phone: two SIP accounts share a local port; only the first runs",
				"port", a.LocalPort, "running", cur.String(), "skipped", a.String())
			return
		}
		if _, ok := out[a.LocalPort]; !ok {
			owners[a.LocalPort] = owner
		}
		out[a.LocalPort] = a
	}
	if kind, err := s.providerKind(ctx, ""); err == nil && kind == ProviderSIP {
		if a, _, err := s.sipAccount(ctx, ""); err == nil {
			add(a, "")
		}
	}
	households := map[string]bool{}
	for _, key := range []string{SettingProvider, SettingSIPServer, SettingSIPExtension, SettingSIPPassword} {
		vals, err := s.Settings.HouseholdValues(ctx, key)
		if err != nil {
			continue
		}
		for _, v := range vals {
			households[v.HouseholdID] = true
		}
	}
	ids := make([]string, 0, len(households))
	for id := range households {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, hh := range ids {
		if kind, err := s.providerKind(ctx, hh); err != nil || kind != ProviderSIP {
			continue
		}
		if a, source, err := s.sipAccount(ctx, hh); err == nil {
			owner := hh
			if source == SourceSystem {
				owner = ""
			}
			add(a, owner)
		}
	}
	return out, owners
}

// reconcileSIP starts a UA for each wanted account that has none, and closes those no longer
// wanted (they unregister). A UA that stopped after authentication failures stays stopped
// until its account changes: retrying would only feed the PBX's firewall.
func (s *Service) reconcileSIP(ctx context.Context) {
	s.init()
	s.sip.reconcile.Lock()
	defer s.sip.reconcile.Unlock()
	want, owners := s.wantedSIP(ctx)
	s.sip.mu.Lock()
	var stale []sipUA
	for port, l := range s.sip.lines {
		if a, ok := want[port]; !ok || a != l.acct {
			if l.ua != nil {
				stale = append(stale, l.ua)
			}
			delete(s.sip.lines, port)
		}
	}
	s.sip.mu.Unlock()
	for _, ua := range stale {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		ua.Close(cctx)
		cancel()
	}
	start := s.StartSIP
	if start == nil {
		start = func(ctx context.Context, a SIPAccount) (sipUA, error) {
			return startRealUA(ctx, a, func(c sip.Config) sip.Config { c.Log = s.log(); return c })
		}
	}
	base := s.baseCtx()
	for port, a := range want {
		s.sip.mu.Lock()
		l := s.sip.lines[port]
		s.sip.mu.Unlock()
		if l != nil && l.ua != nil {
			continue // running (a failed start is retried)
		}
		ua, err := start(base, a)
		if err != nil {
			s.log().Error("phone: SIP account could not start", "account", a.String(), "err", err)
		} else {
			s.log().Info("phone: SIP account started", "account", a.String())
		}
		s.sip.mu.Lock()
		s.sip.lines[port] = &sipLine{acct: a, owner: owners[port], ua: ua, err: err}
		s.sip.mu.Unlock()
	}
}

// runSIP keeps the UAs in step with the settings until ctx ends, then closes them all.
func (s *Service) runSIP(ctx context.Context) {
	s.reconcileSIP(ctx)
	t := time.NewTicker(orDefault(s.SIPReconcileEvery, sipReconcileEvery))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.closeSIP()
			return
		case <-t.C:
			s.reconcileSIP(ctx)
		}
	}
}

func (s *Service) closeSIP() {
	s.sip.mu.Lock()
	lines := s.sip.lines
	s.sip.lines = map[int]*sipLine{}
	s.sip.mu.Unlock()
	var wg sync.WaitGroup
	for _, l := range lines {
		if l.ua == nil {
			continue
		}
		wg.Add(1)
		go func(ua sipUA) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ua.Close(ctx)
		}(l.ua)
	}
	wg.Wait()
}

// sipCallAdapter is a SIP call as the live loop's Call.
type sipCallAdapter struct{ c sipCall }

func (a *sipCallAdapter) ID() string                       { return a.c.ID() }
func (a *sipCallAdapter) Started() <-chan struct{}         { return a.c.Started() }
func (a *sipCallAdapter) Frames() <-chan []int16           { return a.c.Frames() }
func (a *sipCallAdapter) Played() (int64, <-chan struct{}) { return a.c.Played() }
func (a *sipCallAdapter) Done() <-chan struct{}            { return a.c.Done() }
func (a *sipCallAdapter) Hangup(ctx context.Context) error { return a.c.Hangup(ctx) }
func (a *sipCallAdapter) Close()                           { a.c.Close() }

// Play ignores the cue: the pacer itself knows what it has sent.
func (a *sipCallAdapter) Play(pcm []int16, _ string) (int64, error) { return a.c.Play(pcm) }

func (a *sipCallAdapter) Flush() error {
	a.c.Flush()
	return nil
}

func (a *sipCallAdapter) SendDTMF(digits string) error {
	if err := a.c.SendDTMF(digits); errors.Is(err, sip.ErrUnsupported) {
		return ErrUnsupported
	} else {
		return err
	}
}

func (a *sipCallAdapter) EndReason() EndReason {
	end, _ := a.c.End()
	switch end {
	case sip.EndNone:
		return EndNone
	case sip.EndRemote:
		return EndRemote
	case sip.EndClosed:
		return EndClosed
	case sip.EndBusy:
		return EndBusy
	case sip.EndNoAnswer:
		return EndNoAnswer
	case sip.EndNotFound:
		return EndNotFound
	case sip.EndNoMedia:
		return EndNoMedia
	case sip.EndTransport:
		return EndTransport
	}
	return EndFailed
}

// EndDetail is the SIP response or watchdog behind EndReason ("486 Busy Here").
func (a *sipCallAdapter) EndDetail() string {
	_, d := a.c.End()
	return d
}

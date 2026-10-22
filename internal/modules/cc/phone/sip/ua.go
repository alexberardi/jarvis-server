package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// UA is one SIP account's user agent: its socket on the stable local port, its registration
// supervisor and its calls.
type UA struct {
	cfg       Config
	ua        *sipgo.UserAgent
	client    *sipgo.Client
	server    *sipgo.Server
	dg        *diago.Diago
	bindHost  string
	contact   sip.ContactHeader
	registrar sip.Uri

	cancel context.CancelFunc // stops the supervisor
	sock   interface{ Close() error }
	served chan struct{} // the server has stopped reading the socket
	done   chan struct{} // the supervisor has exited (after its unregister)

	mu        sync.Mutex
	status    Status
	changed   chan struct{} // closed at the next status change
	allowed   []net.IP      // the registrar's addresses: the only peers we answer
	calls     map[string]*Call
	closeOnce sync.Once

	refresh chan struct{} // Check: re-register now
	trace   *traceAccount // this UA in the process-wide SIP trace
}

// Start opens cfg's socket and starts registering. It fails at once on a bad config or a
// local port in use; the registration's own failures are in Status.
func Start(ctx context.Context, cfg Config) (*UA, error) {
	cfg.defaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	u := &UA{cfg: cfg, calls: map[string]*Call{}, changed: make(chan struct{}), done: make(chan struct{}),
		refresh: make(chan struct{}, 1)}
	u.status = Status{State: StateRegistering, Since: time.Now()}
	if err := u.resolveRegistrar(ctx); err != nil {
		return nil, err
	}
	bind := cfg.BindHost
	if bind == "" {
		var err error
		if bind, err = routeTo(u.allowed[0], cfg.Port); err != nil {
			return nil, err
		}
	}
	u.bindHost = bind

	var err error
	if u.ua, err = sipgo.NewUA(sipgo.WithUserAgent(cfg.Extension), sipgo.WithUserAgentHostname(bind)); err != nil {
		return nil, fmt.Errorf("sip: %w", err)
	}
	opts := []sipgo.ClientOption{sipgo.WithClientNAT(), sipgo.WithClientLogger(cfg.Log)}
	if cfg.Transport == "udp" {
		// Requests leave from the listening socket, so Via and Contact carry the stable port.
		opts = append(opts, sipgo.WithClientConnectionAddr(net.JoinHostPort(bind, strconv.Itoa(cfg.LocalPort))))
	}
	if u.client, err = sipgo.NewClient(u.ua, opts...); err != nil {
		u.ua.Close()
		return nil, fmt.Errorf("sip: %w", err)
	}
	if u.server, err = sipgo.NewServer(u.ua, sipgo.WithServerLogger(cfg.Log)); err != nil {
		u.ua.Close()
		return nil, fmt.Errorf("sip: %w", err)
	}
	u.dg = diago.NewDiago(u.ua,
		diago.WithLogger(cfg.Log),
		diago.WithClient(u.client),
		diago.WithServer(u.server),
		diago.WithTransport(diago.Transport{Transport: cfg.Transport, BindHost: bind, BindPort: cfg.LocalPort}),
		diago.WithServerRequestMiddleware(u.guard),
	)
	u.server.OnUpdate(u.guard(u.handleUpdate))
	u.contact = sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: cfg.Extension, Host: bind, Port: cfg.LocalPort,
		UriParams: sip.NewParams(), Headers: sip.NewParams()}}
	if cfg.Transport != "udp" {
		u.contact.Address.UriParams.Add("transport", cfg.Transport)
	}

	// We own the socket (not diago's ServeBackground) so Close releases it before returning:
	// a restart binds the same port, and so keeps the same Contact. It outlives the
	// supervisor, whose last act is the unregister.
	if err := u.listen(); err != nil {
		u.ua.Close()
		return nil, err
	}
	u.dg.HandleFunc(u.handleInvite)
	u.trace = u.newTraceAccount()
	processTracer.add(u.trace)
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	u.cancel = cancel
	go u.supervise(sctx)
	// The owner's ctx ending is a shutdown.
	go func() {
		select {
		case <-ctx.Done():
			u.Close(context.Background())
		case <-u.done:
		}
	}()
	return u, nil
}

// listen binds the account's local port and serves it.
func (u *UA) listen() error {
	addr := net.JoinHostPort(u.bindHost, strconv.Itoa(u.cfg.LocalPort))
	u.served = make(chan struct{})
	if u.cfg.Transport == "tcp" {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("sip: listening on %s/tcp: %w", addr, err)
		}
		u.sock = l
		go func() { defer close(u.served); _ = u.server.ServeTCP(l) }()
		return nil
	}
	c, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("sip: listening on %s/udp: %w", addr, err)
	}
	u.sock = c
	go func() { defer close(u.served); _ = u.server.ServeUDP(c) }()
	return nil
}

// resolveRegistrar looks the registrar up: its addresses are the only peers we answer.
func (u *UA) resolveRegistrar(ctx context.Context) error {
	host := strings.TrimSpace(u.cfg.Server)
	u.registrar = sip.Uri{Scheme: "sip", Host: host, Port: u.cfg.Port, UriParams: sip.NewParams(), Headers: sip.NewParams()}
	if u.cfg.Transport != "udp" {
		u.registrar.UriParams.Add("transport", u.cfg.Transport)
	}
	if ip := net.ParseIP(host); ip != nil {
		u.allowed = []net.IP{ip}
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(rctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("sip: cannot resolve the PBX %q: %v", host, err)
	}
	for _, a := range addrs {
		u.allowed = append(u.allowed, a.IP)
	}
	return nil
}

// routeTo is the local address the kernel would send from to reach ip (no packet is sent).
func routeTo(ip net.IP, port int) (string, error) {
	c, err := net.Dial("udp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("sip: no route to the PBX %s: %w", ip, err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// Config is the UA's account (defaults applied).
func (u *UA) Config() Config { return u.cfg }

// Status is the registration's state now.
func (u *UA) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.status
	st.ActiveCalls = len(u.calls)
	return st
}

// Changed is closed at the next status change.
func (u *UA) Changed() <-chan struct{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.changed
}

func (u *UA) setStatus(f func(s *Status)) {
	u.mu.Lock()
	prev := u.status.State
	f(&u.status)
	if u.status.State != prev {
		u.status.Since = time.Now()
	}
	close(u.changed)
	u.changed = make(chan struct{})
	u.mu.Unlock()
}

// Ready is nil when a call can be placed: the UA is registered.
func (u *UA) Ready() error {
	st := u.Status()
	switch st.State {
	case StateRegistered:
		return nil
	case StateStopped:
		return fmt.Errorf("the PBX refused this extension's registration (%s). %s", st.LastError, st.Hint)
	case StateClosed:
		return errors.New("the SIP connection to the PBX is shut down")
	}
	if st.LastError != "" {
		return fmt.Errorf("%w (last attempt: %s)", ErrNotRegistered, st.LastError)
	}
	return ErrNotRegistered
}

// Close unregisters (Expires: 0, so the PBX takes a new Contact at once on the next start),
// ends every call and releases the socket. Safe to call more than once.
func (u *UA) Close(ctx context.Context) {
	u.closeOnce.Do(func() {
		u.mu.Lock()
		calls := make([]*Call, 0, len(u.calls))
		for _, c := range u.calls {
			calls = append(calls, c)
		}
		u.mu.Unlock()
		for _, c := range calls {
			c.Close()
		}
		u.cancel() // the supervisor unregisters on its way out
		select {
		case <-u.done:
		case <-ctx.Done():
		}
		u.setStatus(func(s *Status) { s.State = StateClosed })
		_ = u.sock.Close()
		<-u.served
		processTracer.remove(u.trace)
		u.client.Close()
		u.server.Close()
		u.ua.Close()
	})
}

// Done is closed once the UA has unregistered and stopped (Close or the start ctx).
func (u *UA) Done() <-chan struct{} { return u.done }

// fromRegistrar reports whether req came from the registrar (by address: scanners elsewhere
// get nothing but a 403).
func (u *UA) fromRegistrar(req *sip.Request) bool {
	host, _, err := net.SplitHostPort(req.Source())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	for _, a := range u.allowed {
		if a.Equal(ip) {
			return true
		}
	}
	return false
}

// guard wraps every request handler: requests from anywhere but the registrar get 403, and
// a panic (remote SDP has crashed diago's parsing before) is a 500 for that request only.
func (u *UA) guard(next sipgo.RequestHandler) sipgo.RequestHandler {
	return func(req *sip.Request, tx sip.ServerTransaction) {
		defer func() {
			if p := recover(); p != nil {
				u.cfg.Log.Error("sip: request handler panicked", "method", req.Method.String(), "panic", p)
				respond(req, tx, sip.StatusInternalServerError, "Server Internal Error")
			}
		}()
		if !u.fromRegistrar(req) {
			u.cfg.Log.Warn("sip: refused a request from outside the PBX", "method", req.Method.String(), "from", req.Source())
			respond(req, tx, sip.StatusForbidden, "Forbidden")
			return
		}
		if req.Method == sip.INVITE && req.To() != nil {
			if _, ok := req.To().Params.Get("tag"); ok && !u.canonicalReinvite(req, tx) {
				return
			}
		}
		next(req, tx)
	}
}

// canonicalReinvite rewrites an in-dialog INVITE's SDP (canonicalSDP) before diago applies
// it, or answers 488 when it is unusable. A re-INVITE with no SDP (a session refresh) passes.
func (u *UA) canonicalReinvite(req *sip.Request, tx sip.ServerTransaction) bool {
	body := req.Body()
	if len(body) == 0 {
		return true
	}
	r, err := canonicalSDP(body)
	if err != nil {
		u.cfg.Log.Warn("sip: refused a re-INVITE's SDP", "err", err)
		respond(req, tx, sip.StatusNotAcceptableHere, "Not Acceptable Here")
		return false
	}
	req.SetBody(r.body)
	if c := u.call(req.CallID()); c != nil {
		c.remoteMedia(r)
	}
	return true
}

func respond(req *sip.Request, tx sip.ServerTransaction, code int, reason string) {
	if tx == nil || req.Method == sip.ACK {
		return
	}
	_ = tx.Respond(sip.NewResponseFromRequest(req, code, reason, nil))
}

// handleInvite answers a new inbound call. Inbound calls are a later step
// (docs/cc/phone-sip.md §4 step 3): 480 sends the caller on to the PBX's next destination.
// What the INVITE said is kept for the inbound watch self-test.
func (u *UA) handleInvite(d *diago.DialogServerSession) {
	in := inboundInvite(d.InviteRequest)
	in.Response = sip.StatusTemporarilyUnavailable
	u.setStatus(func(s *Status) { s.LastInbound = in })
	u.cfg.Log.Info("sip: inbound call refused (not supported yet)", "from", in.Caller)
	if err := d.Respond(sip.StatusTemporarilyUnavailable, "Temporarily Unavailable", nil); err != nil {
		u.cfg.Log.Warn("sip: refusing an inbound call failed", "err", err)
	}
}

// inboundInvite reads an INVITE's caller (P-Asserted-Identity, then From: a hint, never
// authentication) and its audio offer.
func inboundInvite(req *sip.Request) InboundInvite {
	in := InboundInvite{At: time.Now()}
	if to := req.To(); to != nil {
		in.To = to.Address.User
	}
	if h := req.GetHeader("P-Asserted-Identity"); h != nil {
		var uri sip.Uri
		params := sip.NewParams()
		if name, err := sip.ParseAddressValue(h.Value(), &uri, &params); err == nil {
			in.Caller, in.CallerName = uri.User, name
		}
	}
	if from := req.From(); in.Caller == "" && from != nil {
		in.Caller, in.CallerName = from.Address.User, from.DisplayName
	}
	in.Media, in.Codecs = sdpAudio(req.Body())
	return in
}

// handleUpdate answers UPDATE (a session-timer refresh, RFC 4028/3311) within a live call
// with the SDP we already use: nothing about the media changes.
func (u *UA) handleUpdate(req *sip.Request, tx sip.ServerTransaction) {
	c := u.call(req.CallID())
	if c == nil {
		respond(req, tx, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	var body []byte
	if len(req.Body()) > 0 {
		r, err := canonicalSDP(req.Body())
		if err != nil {
			respond(req, tx, sip.StatusNotAcceptableHere, "Not Acceptable Here")
			return
		}
		c.remoteMedia(r)
		body = c.localSDP()
	}
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", body)
	if body != nil {
		res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	}
	res.AppendHeader(u.contact.Clone())
	_ = tx.Respond(res)
}

func (u *UA) call(id *sip.CallIDHeader) *Call {
	if id == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[id.Value()]
}

func (u *UA) track(c *Call) {
	u.mu.Lock()
	u.calls[c.id] = c
	u.mu.Unlock()
}

func (u *UA) untrack(c *Call) {
	u.mu.Lock()
	if u.calls[c.id] == c {
		delete(u.calls, c.id)
	}
	u.mu.Unlock()
}

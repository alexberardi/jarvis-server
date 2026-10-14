package node

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Runner is a node's lifetime: discover jarvisd, fetch the broker credential, connect to MQTT as
// jarvis-node-{id} with a persistent session, heartbeat, and dispatch commands until ctx ends.
type Runner struct {
	Creds       Creds
	Tools       Toolset
	Version     string
	InstallMode string // reported in heartbeats, e.g. "desktop-agent"
	NodeKind    string // reported in heartbeats: "desktop" for the desktop agent
	// Capabilities, if set, is asked for the heartbeat's capabilities on every beat.
	Capabilities func() map[string]any
	// Changed, if set, returns a channel that is closed when a capability changes: the next
	// heartbeat goes out at once instead of at the next tick (the admin's ticks turn green
	// within seconds of the browser extension connecting).
	Changed   func() <-chan struct{}
	Log       *slog.Logger
	Heartbeat time.Duration
	// OnConnected, if set, runs in its own goroutine once jarvisd is discovered and the
	// node is connecting (the voice pipeline starts here). It must return when ctx ends; Run
	// waits for it.
	OnConnected func(ctx context.Context, cc *CC)
	// Topics are extra sub-topics of jarvis/nodes/{id}/ to subscribe to (e.g. "k2/provision"),
	// each with its handler. Handlers run on their own goroutine.
	Topics map[string]func(ctx context.Context, cc *CC, payload []byte)
	// OnMQTTConnect, if set, runs on its own goroutine after every MQTT (re)connect: the
	// backstop for nudges sent while the node had no session.
	OnMQTTConnect func(ctx context.Context, cc *CC)
	// NeedsK2, if set, is asked for each heartbeat's needs_k2.
	NeedsK2 func() bool

	kickOnce sync.Once
	kick     chan struct{}

	reqOnce   sync.Once
	signOut   chan struct{} // SignOut asks Run to sign this node out
	recheckCh chan struct{} // a "signed-out" message asks Run to check the key at once
}

func (r *Runner) kicks() chan struct{} {
	r.kickOnce.Do(func() { r.kick = make(chan struct{}, 1) })
	return r.kick
}

// BeatNow sends the next heartbeat at once rather than at the next tick (e.g. needs_k2 just
// changed). Safe to call from any goroutine, before or during Run.
func (r *Runner) BeatNow() {
	select {
	case r.kicks() <- struct{}{}:
	default: // one is already due
	}
}

// SignedOutTopic is where jarvisd tells a desktop agent it was signed out (cc signout.go). The
// message is only a nudge: Run checks its key with a heartbeat and signs out only if jarvisd
// refuses it, so a spoofed message can't sign a working node out.
func SignedOutTopic(nodeID string) string { return "jarvis/nodes/" + nodeID + "/signed-out" }

func (r *Runner) chans() {
	r.reqOnce.Do(func() {
		r.signOut = make(chan struct{}, 1)
		r.recheckCh = make(chan struct{}, 1)
	})
}

// SignOut asks a running Run to sign this node out of jarvisd (the status icon's "Sign out of
// Jarvis Automation"): it removes the node with its own key and returns ErrSignedOut. Before
// jarvisd is reached, Run returns ErrSignedOut without the server call.
func (r *Runner) SignOut() {
	r.chans()
	select {
	case r.signOut <- struct{}{}:
	default:
	}
}

// recheck asks Run for a heartbeat now (the "signed-out" nudge).
func (r *Runner) recheck() {
	r.chans()
	select {
	case r.recheckCh <- struct{}{}:
	default:
	}
}

// brokerURL maps jarvisd's mqtt[s]/ws[s] URL to paho's scheme.
func brokerURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("bad MQTT broker URL %q", raw)
	}
	port := u.Port()
	scheme := map[string]string{"mqtt": "tcp", "tcp": "tcp", "mqtts": "ssl", "ssl": "ssl", "ws": "ws", "wss": "wss"}[u.Scheme]
	if scheme == "" {
		return "", fmt.Errorf("unsupported MQTT scheme %q", u.Scheme)
	}
	if port == "" {
		port = map[string]string{"tcp": "1883", "ssl": "8883", "ws": "80", "wss": "443"}[scheme]
	}
	return fmt.Sprintf("%s://%s:%s%s", scheme, u.Hostname(), port, u.Path), nil
}

// Run blocks until ctx is cancelled. Transient failures (jarvisd restarting, network) retry
// with backoff rather than exiting, so a supervisor isn't needed for blips.
//
// Run returns ErrSignedOut when jarvisd refuses this node's key (the node was signed out or
// removed) or after SignOut; the caller drops the credentials and shows the setup page.
func (r *Runner) Run(ctx context.Context) error {
	r.chans()
	hc := &http.Client{Timeout: 15 * time.Second}
	var svcs Services
	backoff := time.Second
	for {
		var err error
		svcs, err = Discover(ctx, hc, r.Creds.ConfigURL)
		if err == nil {
			_, e1 := svcs.Need(SvcCommandCenter)
			_, e2 := svcs.Need(SvcMQTT)
			if err = e1; err == nil {
				err = e2
			}
		}
		if err == nil {
			break
		}
		r.Log.Warn("jarvisd not reachable yet; retrying", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-r.signOut:
			r.Log.Warn("signing out without reaching jarvisd: the computer stays listed until it is removed in Add-ons")
			return ErrSignedOut
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
	ccURL, _ := svcs.Need(SvcCommandCenter)
	mqURL, _ := svcs.Need(SvcMQTT)
	cc := NewCC(ccURL, r.Creds)

	// The channel is taken before each beat is built, so a change during a beat wakes the next.
	watch := func() <-chan struct{} {
		if r.Changed == nil {
			return nil // blocks forever
		}
		return r.Changed()
	}
	changed := watch()

	// An early heartbeat proves the key works (and marks the node contacted) before MQTT.
	if err := cc.Heartbeat(ctx, r.beat()); err != nil {
		if Revoked(err) {
			r.Log.Warn("jarvisd refuses this node's key: signed out", "err", err)
			return ErrSignedOut
		}
		r.Log.Warn("first heartbeat failed", "err", err)
	}
	mc, err := cc.MQTTCredentials(ctx)
	if Revoked(err) {
		r.Log.Warn("jarvisd refuses this node's key: signed out", "err", err)
		return ErrSignedOut
	}
	if err != nil {
		return fmt.Errorf("mqtt credentials: %w", err)
	}
	broker, err := brokerURL(mqURL)
	if err != nil {
		return err
	}

	d := &Dispatcher{CC: cc, Tools: r.Tools, Log: r.Log}
	topic := "jarvis/nodes/" + r.Creds.NodeID + "/commands"
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID("jarvis-node-" + r.Creds.NodeID). // the broker requires exactly this id
		SetCleanSession(false).                       // QoS-1 commands queue while we're away
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(time.Minute).
		SetKeepAlive(30 * time.Second).
		SetOrderMatters(false) // handlers run concurrently; a slow tool can't stall the client
	if mc.Username != "" {
		opts.SetUsername(mc.Username).SetPassword(mc.Password)
	}
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		r.Log.Info("mqtt connected", "broker", broker, "topic", topic)
		tok := c.Subscribe(topic, 1, func(_ mqtt.Client, m mqtt.Message) {
			d.Handle(ctx, m.Payload())
		})
		if tok.WaitTimeout(10*time.Second) && tok.Error() != nil {
			r.Log.Error("mqtt subscribe failed", "err", tok.Error())
		}
		for sub, h := range r.Topics {
			tok := c.Subscribe("jarvis/nodes/"+r.Creds.NodeID+"/"+sub, 1, func(_ mqtt.Client, m mqtt.Message) {
				go h(ctx, cc, m.Payload())
			})
			if tok.WaitTimeout(10*time.Second) && tok.Error() != nil {
				r.Log.Error("mqtt subscribe failed", "topic", sub, "err", tok.Error())
			}
		}
		if r.OnMQTTConnect != nil {
			go r.OnMQTTConnect(ctx, cc)
		}
		so := c.Subscribe(SignedOutTopic(r.Creds.NodeID), 1, func(mqtt.Client, mqtt.Message) {
			r.Log.Info("jarvisd says this computer was signed out; checking the key")
			r.recheck()
		})
		if so.WaitTimeout(10*time.Second) && so.Error() != nil {
			r.Log.Error("mqtt subscribe failed", "topic", "signed-out", "err", so.Error())
		}
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		r.Log.Warn("mqtt connection lost; reconnecting", "err", err)
	})
	client := mqtt.NewClient(opts)
	client.Connect() // with ConnectRetry it keeps trying in the background
	defer client.Disconnect(250)

	if r.OnConnected != nil {
		// OnConnected returns when its ctx ends, so Run ending on its own (a sign-out) must end
		// that ctx before waiting for it.
		octx, stop := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.OnConnected(octx, cc)
		}()
		defer func() {
			stop()
			wg.Wait()
		}()
	}

	every := r.Heartbeat
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.signOut:
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := cc.SignOut(sctx)
			cancel()
			if err != nil {
				r.Log.Warn("couldn't tell jarvisd; signing out here anyway (the computer stays listed until it is removed in Add-ons)", "err", err)
			} else {
				r.Log.Info("signed out of jarvisd")
			}
			return ErrSignedOut
		case <-t.C:
		case <-changed:
		case <-r.kicks():
		case <-r.recheckCh:
		}
		changed = watch()
		if err := cc.Heartbeat(ctx, r.beat()); err != nil && ctx.Err() == nil {
			if Revoked(err) {
				r.Log.Warn("jarvisd refuses this node's key: signed out", "err", err)
				return ErrSignedOut
			}
			r.Log.Warn("heartbeat failed", "err", err)
		}
	}
}

func (r *Runner) beat() Beat {
	b := Beat{Version: r.Version, InstallMode: r.InstallMode, NodeKind: r.NodeKind}
	if r.NeedsK2 != nil {
		b.NeedsK2 = r.NeedsK2()
	}
	if r.Capabilities != nil {
		b.Capabilities = r.Capabilities()
	}
	return b
}

package node

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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
	Log         *slog.Logger
	Heartbeat   time.Duration
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
func (r *Runner) Run(ctx context.Context) error {
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
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
	ccURL, _ := svcs.Need(SvcCommandCenter)
	mqURL, _ := svcs.Need(SvcMQTT)
	cc := NewCC(ccURL, r.Creds)

	// An early heartbeat proves the key works (and marks the node contacted) before MQTT.
	if err := cc.Heartbeat(ctx, r.Version, r.InstallMode); err != nil {
		if he, ok := err.(*HTTPError); ok && (he.Status == 401 || he.Status == 403) {
			return fmt.Errorf("jarvisd rejected this node's key (%v); re-enroll", err)
		}
		r.Log.Warn("first heartbeat failed", "err", err)
	}
	mc, err := cc.MQTTCredentials(ctx)
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
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		r.Log.Warn("mqtt connection lost; reconnecting", "err", err)
	})
	client := mqtt.NewClient(opts)
	client.Connect() // with ConnectRetry it keeps trying in the background
	defer client.Disconnect(250)

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
		case <-t.C:
			if err := cc.Heartbeat(ctx, r.Version, r.InstallMode); err != nil && ctx.Err() == nil {
				r.Log.Warn("heartbeat failed", "err", err)
			}
		}
	}
}

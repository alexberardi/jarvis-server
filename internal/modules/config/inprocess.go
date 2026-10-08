package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// In-process registry access for the admin Connections page (AD7): list the rows with what
// manages them, probe them, and add or remove the operator's external entries. The admin-token
// HTTP routes keep their legacy behaviour.

// What manages a registry row.
const (
	ManagedListener = "listener" // a listener jarvisd serves (synced at startup)
	ManagedBroker   = "broker"   // the embedded MQTT broker
	ManagedSetting  = "setting"  // synced from a jarvisd setting (e.g. jarvis-pantry)
	// "" is an operator-added external entry.
)

// ServiceEntry is a registry row as the admin shows it.
type ServiceEntry struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Scheme      string `json:"scheme"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	HealthPath  string `json:"health_path"`
	Description string `json:"description"`
	Managed     string `json:"managed"`
	// PublicURL is the operator's public base URL (public.go) as entered, "" when unset.
	PublicURL string `json:"public_url"`
	svc       service
}

// NewService is an external entry to add: a base URL (scheme://host[:port], no path).
type NewService struct {
	Name        string
	URL         string
	HealthPath  string // "" is /health
	Description string
	PublicURL   string // optional public base URL (public.go)
}

var (
	// ErrServiceExists is a name already in the registry.
	ErrServiceExists = errors.New("config: service already exists")
	// ErrServiceNotFound is an unknown name.
	ErrServiceNotFound = errors.New("config: service not found")
	// ErrServiceManaged is a row jarvisd maintains (it would come back at the next start).
	ErrServiceManaged = errors.New("config: service is managed by jarvisd")
)

// ValidationError lists the invalid fields of a NewService (FastAPI-style loc ["body", field]).
type ValidationError struct{ Fields []httpx.FieldError }

func (e *ValidationError) Error() string {
	var msgs []string
	for _, f := range e.Fields {
		msgs = append(msgs, fmt.Sprint(f.Loc[len(f.Loc)-1])+": "+f.Msg)
	}
	return "config: invalid service: " + strings.Join(msgs, "; ")
}

// managed maps each jarvisd-maintained name to what maintains it.
func (m *Module) managed(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, l := range m.Served {
		if name, ok := ServiceNames[l]; ok {
			out[name] = ManagedListener
		}
	}
	if m.MQTTPort > 0 {
		out["jarvis-mqtt-broker"] = ManagedBroker
	}
	if m.External != nil {
		for name := range m.External(ctx) {
			out[name] = ManagedSetting
		}
	}
	return out
}

// Services lists every registry row, sorted by name.
func (m *Module) Services(ctx context.Context) ([]ServiceEntry, error) {
	svcs, err := m.all(ctx)
	if err != nil {
		return nil, err
	}
	managed := m.managed(ctx)
	out := make([]ServiceEntry, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, m.toEntry(s, managed))
	}
	return out, nil
}

// toEntry is a row as the admin shows it: its LAN URL, plus the public one when set.
func (m *Module) toEntry(s service, managed map[string]string) ServiceEntry {
	return ServiceEntry{
		Name: s.Name, URL: s.url(urlStyle{}), Scheme: s.Scheme, Host: s.Host, Port: s.Port,
		HealthPath: s.HealthPath.String, Description: s.Description.String, Managed: managed[s.Name],
		PublicURL: s.publicDisplay(), svc: s,
	}
}

// Probe checks one entry: GET its health path for http(s) rows, a TCP connect for the rest
// (the MQTT broker has no HTTP health route).
func (m *Module) Probe(ctx context.Context, e ServiceEntry) HealthStatus {
	if e.Scheme == "http" || e.Scheme == "https" {
		return m.probe(ctx, e.svc)
	}
	timeout := m.HealthTimeout
	if secs := m.settings.Float(ctx, "health_check.timeout", settings.Scope{}); secs > 0 {
		timeout = time.Duration(secs * float64(time.Second))
	}
	start := time.Now()
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(e.Host, strconv.Itoa(e.Port)))
	if err != nil {
		msg := "Connection refused"
		if isTimeout(err) {
			msg = "Timeout"
		} else if !strings.Contains(err.Error(), "refused") {
			msg = err.Error()
		}
		return HealthStatus{Error: &msg}
	}
	conn.Close()
	lat := float64(time.Since(start).Microseconds()) / 1000
	lat = float64(int64(lat*100+0.5)) / 100
	return HealthStatus{Healthy: true, LatencyMS: &lat}
}

// ProbeAll probes the entries concurrently, keyed by name.
func (m *Module) ProbeAll(ctx context.Context, es []ServiceEntry) map[string]HealthStatus {
	type res struct {
		name string
		h    HealthStatus
	}
	ch := make(chan res, len(es))
	for _, e := range es {
		go func() { ch <- res{e.Name, m.Probe(ctx, e)} }()
	}
	out := make(map[string]HealthStatus, len(es))
	for range es {
		r := <-ch
		out[r.name] = r.h
	}
	return out
}

// AddService registers an external entry. Errors: *ValidationError, ErrServiceExists,
// ErrServiceManaged (a name jarvisd maintains).
func (m *Module) AddService(ctx context.Context, n NewService) (ServiceEntry, error) {
	var fields []httpx.FieldError
	bad := func(field, typ, msg string, input any) {
		fields = append(fields, httpx.FieldError{Type: typ, Loc: []any{"body", field}, Msg: msg, Input: input})
	}
	name := strings.TrimSpace(n.Name)
	in := serviceInput{Name: &name}
	var scheme string
	var port int
	u, err := url.Parse(strings.TrimSpace(n.URL))
	switch {
	case n.URL == "":
		bad("url", "missing", "Field required", nil)
	case err != nil || u.Scheme == "" || u.Host == "":
		bad("url", "url_parsing", "Input should be a URL like http://host:port", n.URL)
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/"):
		bad("url", "value_error", "The URL must be a base URL: scheme, host and port only", n.URL)
	default:
		scheme = u.Scheme
		host := u.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		in.Host, in.Scheme = &host, &scheme
		port, _ = strconv.Atoi(u.Port())
		if port == 0 {
			port = map[string]int{"http": 80, "https": 443, "ws": 80, "wss": 443, "mqtt": 1883, "mqtts": 8883}[scheme]
		}
		if u.Port() != "" && port == 0 {
			bad("url", "value_error", "The URL's port is not a number", n.URL)
		}
		in.Port = &port
	}
	health := strings.TrimSpace(n.HealthPath)
	if health == "" {
		health = "/health"
	}
	if !strings.HasPrefix(health, "/") {
		bad("health_path", "value_error", "The health path must start with /", n.HealthPath)
	}
	in.HealthPath = &health
	desc := strings.TrimSpace(n.Description)
	in.Description = &desc
	for _, f := range in.validate(true) {
		if f.Loc[1] == "host" || f.Loc[1] == "scheme" || f.Loc[1] == "port" {
			f.Loc = []any{"body", "url"}
		}
		fields = append(fields, f)
	}
	var pub *publicParts
	if strings.TrimSpace(n.PublicURL) != "" && scheme != "" {
		p, errs := parsePublicURL(n.PublicURL, scheme, "public_url")
		fields = append(fields, errs...)
		pub = &p
	}
	if len(fields) > 0 {
		return ServiceEntry{}, &ValidationError{Fields: fields}
	}
	if m.managed(ctx)[name] != "" {
		return ServiceEntry{}, ErrServiceManaged
	}
	var descArg any
	if desc != "" {
		descArg = desc
	}
	_, err = m.deps.DB.Write.ExecContext(ctx, `
		INSERT INTO config_services (name, host, port, scheme, health_path, description)
		VALUES (?, ?, ?, ?, ?, ?)`, name, *in.Host, port, scheme, health, descArg)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ServiceEntry{}, ErrServiceExists
	}
	if err != nil {
		return ServiceEntry{}, err
	}
	if pub != nil {
		if err := m.writePublic(ctx, name, *pub); err != nil {
			return ServiceEntry{}, err
		}
	}
	return m.entry(ctx, name)
}

// RemoveService deletes an operator-added entry. Errors: ErrServiceNotFound,
// ErrServiceManaged (jarvisd would re-create it at the next start).
func (m *Module) RemoveService(ctx context.Context, name string) error {
	if m.managed(ctx)[name] != "" {
		return ErrServiceManaged
	}
	res, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM config_services WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrServiceNotFound
	}
	return nil
}

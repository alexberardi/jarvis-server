// Package config holds jarvisd's bootstrap configuration: where its data lives and which
// address each legacy listener binds. Runtime settings live in the settings DB, not here.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Legacy listener names and their default ports (PLAN §3.1). jarvisd keeps every old port so
// clients need no changes; each listener serves only its own module's routes.
const (
	ListenerConfig        = "config"
	ListenerAuth          = "auth"
	ListenerLogs          = "logs"
	ListenerCC            = "command-center"
	ListenerLLM           = "llm"
	ListenerSTT           = "whisper"
	ListenerTTS           = "tts"
	ListenerNotifications = "notifications"
	ListenerRecipes       = "recipes"
	ListenerOCR           = "ocr"
	ListenerAdmin         = "admin"
)

// DefaultPorts maps each listener to its legacy port.
var DefaultPorts = map[string]int{
	ListenerConfig:        7700,
	ListenerAuth:          7701,
	ListenerLogs:          7702,
	ListenerCC:            7703,
	ListenerLLM:           7704,
	ListenerSTT:           7706,
	ListenerTTS:           7707,
	ListenerNotifications: 7712,
	ListenerRecipes:       7030,
	ListenerOCR:           7031,
	ListenerAdmin:         7710,
}

// Paths is the prefix each listener is also served under on the config port; admin is not
// mounted.
var Paths = map[string]string{
	ListenerAuth:          "/auth",
	ListenerLogs:          "/logs",
	ListenerCC:            "/cc",
	ListenerLLM:           "/llm",
	ListenerSTT:           "/stt",
	ListenerTTS:           "/tts",
	ListenerNotifications: "/notifications",
	ListenerRecipes:       "/recipes",
	ListenerOCR:           "/ocr",
}

type Config struct {
	// Home is the data directory (~/.jarvisd by default): database, blobs, extracted libs, engines.
	Home string
	// Host is the bind address shared by every listener.
	Host string
	// Ports maps listener name to port.
	Ports map[string]int
	// MDNSInterfaces, when set, names the LAN interfaces to advertise on and take the setup
	// link's address from, instead of picking them automatically (netaddr.LAN).
	MDNSInterfaces []string
	// Env is what cmd/jarvisd wires into the modules from the environment.
	Env Env
	// SinglePort: the mounted listeners have no port of their own (legacy ports off); the
	// registry and node URLs use the config port + Paths. Set by the runner before Register.
	SinglePort bool
}

// Env holds the modules' bootstrap values from the environment: secrets, dev switches and the
// MQTT broker's addresses. Runtime settings live in the settings DB.
type Env struct {
	ConfigAdminToken  string // JARVIS_CONFIG_ADMIN_TOKEN
	MDNS              bool   // JARVIS_MDNS: advertise on mDNS unless "0"
	AdminAPIKey       string // ADMIN_API_KEY (notifications and cc)
	RelayHouseholdJWT string // RELAY_HOUSEHOLD_JWT
	DevSpeakerHints   bool   // JARVIS_DEV_SPEAKER_HINTS=1
	// MQTTAddr is JARVIS_MQTT_ADDR when set (even to "", which disables the listener), else the
	// broker's default moved by JARVIS_PORT_OFFSET.
	MQTTAddr string
	// MQTTWSAddr is JARVIS_MQTT_WS_ADDR as set, else "": the WebSocket's 9883 default comes from
	// main, and only when legacy ports are on (server.legacy_ports).
	MQTTWSAddr         string
	MQTTAllowAnonymous bool // JARVIS_MQTT_ALLOW_ANONYMOUS=1
	// MQTTWSAllowedOrigins is JARVIS_MQTT_WS_ALLOWED_ORIGINS as set: comma-separated hosts (or
	// URLs) whose pages may open the broker's WebSocket besides this host, localhost and the LAN.
	MQTTWSAllowedOrigins string
	AuthAdminToken       string // JARVIS_AUTH_ADMIN_TOKEN
	AuthSecretKey        string // AUTH_SECRET_KEY: the legacy HS256 secret
	AdminUIDir           string // JARVIS_ADMIN_UI_DIR
	// AppID and AppKey are jarvisd's legacy app credentials (JARVIS_APP_ID, JARVIS_APP_KEY).
	AppID, AppKey string
	// OSXAPIURL and OSXAPIKey reach Apple Vision on a Mac (JARVIS_OSX_API_URL, JARVIS_OSX_API_KEY).
	OSXAPIURL, OSXAPIKey string
	// The telephony bootstrap: TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN, TWILIO_FROM_NUMBER (cc's
	// phone.EnvTwilio* names), JARVIS_PHONE_PUBLIC_URL and JARVIS_PHONE_PUBLIC_WSS_URL.
	TwilioAccountSID, TwilioAuthToken, TwilioFromNumber string
	PhonePublicURL, PhonePublicWSSURL                   string
	// Container is JARVIS_CONTAINER, trimmed and lower-cased: "" (not a container), "docker"
	// (the image's default) or "unraid" (its template). Updates and Stop then go through the
	// container host.
	Container string
	// LegacyPorts is JARVIS_LEGACY_PORTS as set: "1"/"true" on, "0"/"false" off, "" leaves it to
	// the server.legacy_ports setting (ParseLegacyPorts).
	LegacyPorts string
}

// DBPath is the single SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.Home, "jarvis.db") }

// Addr returns host:port for a listener, or an error for an unknown listener.
func (c Config) Addr(listener string) (string, error) {
	p, ok := c.Ports[listener]
	if !ok {
		return "", fmt.Errorf("config: unknown listener %q", listener)
	}
	return fmt.Sprintf("%s:%d", c.Host, p), nil
}

// Load builds the config from the environment:
//
//	JARVIS_HOME              data directory (default ~/.jarvisd; cmd/jarvisd resolves it with ResolveHome)
//	JARVIS_HOST              bind address (default 0.0.0.0)
//	JARVIS_PORT_OFFSET       added to every default port (the listeners and the MQTT broker), so a
//	                         second instance can run beside an installed one: 10000 puts CC on 17703
//	JARVIS_PORT_<LISTENER>   per-listener port override, e.g. JARVIS_PORT_COMMAND_CENTER=17703;
//	                         taken as is, without the offset
//	JARVIS_MDNS_INTERFACES   comma-separated LAN interface names, e.g. "en0" or "eth0,wlan0"
//	JARVIS_LEGACY_PORTS      "1"/"true" or "0"/"false": also bind every listener's own port (and the
//	                         MQTT WebSocket), overriding the server.legacy_ports setting
//
// and the modules' bootstrap values (Env).
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	getenv := func(k string) string { v, _ := lookup(k); return v }
	c := Config{Home: getenv("JARVIS_HOME"), Host: getenv("JARVIS_HOST"), Ports: map[string]int{}}
	if c.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("config: no JARVIS_HOME and no home dir: %w", err)
		}
		c.Home = filepath.Join(h, DefaultHomeName)
	}
	if c.Host == "" {
		c.Host = "0.0.0.0"
	}
	for _, n := range strings.Split(getenv("JARVIS_MDNS_INTERFACES"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			c.MDNSInterfaces = append(c.MDNSInterfaces, n)
		}
	}
	offset, err := portOffset(getenv)
	if err != nil {
		return Config{}, err
	}
	for name, def := range DefaultPorts {
		c.Ports[name] = def + offset
		key := "JARVIS_PORT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if v := getenv(key); v != "" {
			p, err := strconv.Atoi(v)
			if err != nil || p < 0 || p > 65535 {
				return Config{}, fmt.Errorf("config: %s=%q is not a port", key, v)
			}
			c.Ports[name] = p
		}
	}
	c.Env = loadEnv(lookup, getenv, offset)
	return c, nil
}

// LoadEnv is the modules' bootstrap values from the environment, with the MQTT defaults shifted
// by PortOffset: the Env of a Config that doesn't Load.
func LoadEnv() Env {
	return loadEnv(os.LookupEnv, os.Getenv, PortOffset())
}

func loadEnv(lookup func(string) (string, bool), getenv func(string) string, offset int) Env {
	// envOr keeps a set-but-empty value: "" disables the listener.
	envOr := func(key, def string) string {
		if v, ok := lookup(key); ok {
			return v
		}
		return def
	}
	return Env{
		ConfigAdminToken:     getenv("JARVIS_CONFIG_ADMIN_TOKEN"),
		MDNS:                 getenv("JARVIS_MDNS") != "0",
		AdminAPIKey:          getenv("ADMIN_API_KEY"),
		RelayHouseholdJWT:    getenv("RELAY_HOUSEHOLD_JWT"),
		DevSpeakerHints:      getenv("JARVIS_DEV_SPEAKER_HINTS") == "1",
		MQTTAddr:             envOr("JARVIS_MQTT_ADDR", ShiftAddr(DefaultMQTTAddr, offset)),
		MQTTWSAddr:           envOr("JARVIS_MQTT_WS_ADDR", ""),
		MQTTAllowAnonymous:   getenv("JARVIS_MQTT_ALLOW_ANONYMOUS") == "1",
		MQTTWSAllowedOrigins: getenv("JARVIS_MQTT_WS_ALLOWED_ORIGINS"),
		AuthAdminToken:       getenv("JARVIS_AUTH_ADMIN_TOKEN"),
		AuthSecretKey:        getenv("AUTH_SECRET_KEY"),
		AdminUIDir:           getenv("JARVIS_ADMIN_UI_DIR"),
		AppID:                getenv("JARVIS_APP_ID"),
		AppKey:               getenv("JARVIS_APP_KEY"),
		OSXAPIURL:            getenv("JARVIS_OSX_API_URL"),
		OSXAPIKey:            getenv("JARVIS_OSX_API_KEY"),
		TwilioAccountSID:     getenv("TWILIO_ACCOUNT_SID"),
		TwilioAuthToken:      getenv("TWILIO_AUTH_TOKEN"),
		TwilioFromNumber:     getenv("TWILIO_FROM_NUMBER"),
		PhonePublicURL:       getenv("JARVIS_PHONE_PUBLIC_URL"),
		PhonePublicWSSURL:    getenv("JARVIS_PHONE_PUBLIC_WSS_URL"),
		Container:            strings.ToLower(strings.TrimSpace(getenv("JARVIS_CONTAINER"))),
		LegacyPorts:          getenv("JARVIS_LEGACY_PORTS"),
	}
}

// ParseLegacyPorts reads a JARVIS_LEGACY_PORTS value: set is false for "" (the setting
// decides), and anything but 1/true/0/false is an error.
func ParseLegacyPorts(v string) (set, on bool, err error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return false, false, nil
	case "1", "true":
		return true, true, nil
	case "0", "false":
		return true, false, nil
	}
	return false, false, fmt.Errorf("config: JARVIS_LEGACY_PORTS=%q: want 1, true, 0 or false", v)
}

// The MQTT broker's default listen addresses. They equal mqtt.DefaultTCPAddr and
// mqtt.DefaultWSAddr (a test checks); importing mqtt here would link the broker into every binary.
const (
	DefaultMQTTAddr   = ":1884"
	DefaultMQTTWSAddr = ":9883"
)

// maxPortOffset keeps every shifted default port, the MQTT broker's 9883 the highest, valid.
const maxPortOffset = 65535 - 9883

func portOffset(getenv func(string) string) (int, error) {
	v := getenv("JARVIS_PORT_OFFSET")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxPortOffset {
		return 0, fmt.Errorf("config: JARVIS_PORT_OFFSET=%q is not an offset in 0..%d", v, maxPortOffset)
	}
	return n, nil
}

// PortOffset is JARVIS_PORT_OFFSET, or 0 when it is unset or invalid (Load reports an invalid one).
func PortOffset() int {
	n, _ := portOffset(os.Getenv)
	return n
}

// ShiftAddr adds offset to the port of a host:port address. An empty or unparsable address is
// returned unchanged.
func ShiftAddr(addr string, offset int) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || offset == 0 {
		return addr
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return addr
	}
	return net.JoinHostPort(host, strconv.Itoa(p+offset))
}

// Package config holds jarvisd's bootstrap configuration: where its data lives and which
// address each legacy listener binds. Runtime settings live in the settings DB, not here.
package config

import (
	"fmt"
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

type Config struct {
	// Home is the data directory (~/.jarvis by default): database, blobs, extracted libs, engines.
	Home string
	// Host is the bind address shared by every listener.
	Host string
	// Ports maps listener name to port.
	Ports map[string]int
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
//	JARVIS_HOME              data directory (default ~/.jarvis)
//	JARVIS_HOST              bind address (default 0.0.0.0)
//	JARVIS_PORT_<LISTENER>   per-listener port override, e.g. JARVIS_PORT_COMMAND_CENTER=17703
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	c := Config{Home: getenv("JARVIS_HOME"), Host: getenv("JARVIS_HOST"), Ports: map[string]int{}}
	if c.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("config: no JARVIS_HOME and no home dir: %w", err)
		}
		c.Home = filepath.Join(h, ".jarvis")
	}
	if c.Host == "" {
		c.Host = "0.0.0.0"
	}
	for name, def := range DefaultPorts {
		c.Ports[name] = def
		key := "JARVIS_PORT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if v := getenv(key); v != "" {
			p, err := strconv.Atoi(v)
			if err != nil || p < 0 || p > 65535 {
				return Config{}, fmt.Errorf("config: %s=%q is not a port", key, v)
			}
			c.Ports[name] = p
		}
	}
	return c, nil
}

//go:build contract

package contract

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

// serviceShape is config-service's ServiceResponse.
//
// LEGACY-BUG: created_at/updated_at are naive timestamps (no zone), unlike jarvis-auth's.
var serviceShape = Obj{
	"id":            Int,
	"name":          NonEmptyString,
	"host":          String,
	"port":          Int,
	"scheme":        String,
	"health_path":   NullOr(String),
	"description":   NullOr(String),
	"url":           NonEmptyString,
	"external_host": NullOr(String),
	"external_port": NullOr(Int),
	"created_at":    TimestampNaive,
	"updated_at":    TimestampNaive,
}

// TestConfigInfo: the mobile app's LAN probe (configDiscoveryService.ts probeHost) accepts a
// host only if GET /info returns service == "jarvis-config-service". Unauthenticated.
func TestConfigInfo(t *testing.T) {
	tg := T(t)
	tg.Need(t, Config)
	tg.Get(t, Config, "/info").Expect(http.StatusOK, Obj{"service": Eq("jarvis-config-service")})
}

func servicesByName(t *testing.T, r *Resp) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, s := range r.Object()["services"].([]any) {
		m := s.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

// TestConfigServices freezes GET /services, the discovery document every service
// (jarvis-config-client), node-setup and mobile read at startup.
func TestConfigServices(t *testing.T) {
	tg := T(t)
	tg.Need(t, Config)

	r := tg.Get(t, Config, "/services").Expect(http.StatusOK, Obj{"services": NonEmptyArrayOf(serviceShape)})
	byName := servicesByName(t, r)
	// Mobile gives up unless both of these are present (fetchServiceUrls); every backend
	// resolves jarvis-auth through discovery.
	for _, name := range []string{"jarvis-auth", "jarvis-command-center", "jarvis-config-service", "jarvis-logs"} {
		if _, ok := byName[name]; !ok {
			r.Fatalf("/services is missing %q", name)
		}
	}

	t.Run("style=external rewrites localhost to remote_host", func(t *testing.T) {
		// Mobile asks for style=external and then swaps localhost for the config host itself;
		// node-setup and GPU satellites pass remote_host. With remote_host = the target host,
		// the advertised URLs must actually work from here.
		q := url.Values{"style": {"external"}, "remote_host": {tg.Host}}
		r := tg.Get(t, Config, "/services?"+q.Encode()).Expect(http.StatusOK, Obj{"services": NonEmptyArrayOf(serviceShape)})
		byName := servicesByName(t, r)
		for _, name := range []string{"jarvis-auth", "jarvis-command-center"} {
			raw := byName[name]["url"].(string)
			u, err := url.Parse(raw)
			if err != nil {
				r.Fatalf("%s url %q: %v", name, raw, err)
			}
			if u.Hostname() != tg.Host {
				r.Fatalf("%s url %q: want host %s", name, raw, tg.Host)
			}
			res, err := tg.HTTP.Get(raw + "/health")
			if err != nil {
				t.Fatalf("GET %s/health (advertised by /services): %v", raw, err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET %s/health (advertised by /services): %d", raw, res.StatusCode)
			}
		}
	})

	t.Run("style=dockerized uses host.docker.internal for localhost entries", func(t *testing.T) {
		r := tg.Get(t, Config, "/services?style=dockerized").Expect(http.StatusOK, Obj{"services": NonEmptyArrayOf(serviceShape)})
		for name, s := range servicesByName(t, r) {
			if s["host"] != "localhost" {
				continue
			}
			u, _ := url.Parse(s["url"].(string))
			if u.Hostname() != "host.docker.internal" {
				r.Fatalf("%s: dockerized url %q should use host.docker.internal", name, s["url"])
			}
			if port, _ := strconv.Atoi(u.Port()); u.Port() != "" && float64(port) != mustFloat(s["port"]) {
				r.Fatalf("%s: dockerized url %q should keep port %v", name, s["url"], s["port"])
			}
		}
	})

	t.Run("unknown style is 422", func(t *testing.T) {
		// Mobile relies on this to fall back to plain /services on old config-service builds.
		tg.Get(t, Config, "/services?style=bogus").
			ExpectStatus(http.StatusUnprocessableEntity).
			ExpectShape(ValidationError("query", "style"))
	})
}

func TestConfigServiceByName(t *testing.T) {
	tg := T(t)
	tg.Need(t, Config)
	r := tg.Get(t, Config, "/services/jarvis-auth").Expect(http.StatusOK, serviceShape)
	if r.Object()["name"] != "jarvis-auth" {
		r.Fatalf("name: want jarvis-auth")
	}
	tg.Get(t, Config, "/services/contract-no-such-service").
		ExpectError(http.StatusNotFound, "Service 'contract-no-such-service' not found")
}

func mustFloat(v any) float64 {
	switch n := v.(type) {
	case interface{ Float64() (float64, error) }:
		f, _ := n.Float64()
		return f
	case float64:
		return n
	}
	return -1
}

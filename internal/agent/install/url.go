package install

import (
	"fmt"
	"net/url"
	"strings"
)

// CheckStartURL accepts only jarvis-agent://start (also jarvis-agent:start and a trailing
// slash, as browsers and OSes hand it over). The handler takes no parameters: anything else,
// a query, a fragment, a path, credentials or another action, is refused, so a web page can
// do nothing through the URL but start the agent.
func CheckStartURL(raw string) error {
	bad := fmt.Errorf("the jarvis-agent: link only starts the agent (jarvis-agent://start), not %q", raw)
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, urlScheme) || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.RawFragment != "" || u.Port() != "" {
		return bad
	}
	switch {
	case u.Opaque != "": // jarvis-agent:start
		if u.Opaque != "start" && u.Opaque != "start/" {
			return bad
		}
	case u.Host == "start" && (u.Path == "" || u.Path == "/"):
	default:
		return bad
	}
	return nil
}

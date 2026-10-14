# Public URLs answered in kind

- **Date:** 2026-10-07
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Public URLs answered in kind** (cutover Q1: prod keeps its Cloudflare tunnel). Each registry row may carry an operator-entered public base URL (admin Connections; stored in `external_scheme`/`external_host`/`external_port`, config migration 00002; syncSelf never writes them). `/services` and `/services/{name}` return it when the request's `Host` is one of the public hosts (or, from a loopback peer only, `X-Forwarded-Host`/`CF-Connecting-IP`), and for `?style=external` (the mobile app's style, so phones use the tunnel on the LAN too, as with legacy); `remote`/`dockerized`/default keep LAN URLs. Safe to trust the headers: they only pick which of two operator-entered URLs an unauthenticated read returns, and `?style=external` already returns the public one.

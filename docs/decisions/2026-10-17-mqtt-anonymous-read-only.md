# MQTT anonymous clients are read-only; the WebSocket listener checks Origin

- **Date:** 2026-10-17 (build agent, loop item #123; the user approves the merge)

`JARVIS_MQTT_ALLOW_ANONYMOUS=1` (dev only) used to give a client without credentials the same
access as a trusted server client: subscribe to `#` and publish anywhere, including tool calls
to any node's `jarvis/nodes/{id}/commands`. The ask allowed "read-only or a dev topic prefix";
we chose both: an anonymous client may **subscribe to any topic** (watching traffic is what the
flag is for) but **publish only under `jarvis/dev/`**, which nothing in jarvisd or the nodes
listens to. A node that needs to publish responses connects with its own credential. jarvisd
logs a WARN at start naming both listener addresses, and `jarvisd doctor` (and the admin's
doctor) reports a `mqtt anonymous` warning while it is on. A Last Will topic goes through the
same publish rules: a CONNECT whose will the client could not publish itself is refused (this
also stops a node placing a will on another node's topics).

The WebSocket listener (9883, published through the Cloudflare tunnel on prod) now refuses a
browser page from another site with 403. It admits a request without an `Origin` (nodes are not
browsers), and an Origin whose host is the request's own `Host` (the tunnel keeps the public
name; React Native's WebSocket sends the target URL as its Origin), `localhost`, a loopback,
private or link-local IP, an mDNS `.local` name, or a host listed in
`JARVIS_MQTT_WS_ALLOWED_ORIGINS` (comma-separated hosts or URLs).

Supersedes the "anonymous is unrestricted, matching the legacy Mosquitto setup" note in
`internal/platform/mqtt`. See [doc 05 §5a](../cc/05-nodes.md).

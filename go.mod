module github.com/alexberardi/jarvis-server

go 1.27.1

// The admin SPA's npm dependencies are not Go packages; some ship stray .go files.
ignore ./web/admin/node_modules

require (
	github.com/dlclark/regexp2 v1.12.0
	github.com/ebitengine/purego v0.11.1
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/gorilla/websocket v1.5.0
	github.com/grandcat/zeroconf v1.0.0
	github.com/mochi-mqtt/server/v2 v2.7.9
	github.com/pressly/goose/v3 v3.26.0
	golang.org/x/crypto v0.40.0
	golang.org/x/sys v0.48.0
	modernc.org/sqlite v1.60.1
)

require (
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/miekg/dns v1.1.27 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rs/xid v1.4.0 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/net v0.42.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

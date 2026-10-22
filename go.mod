module github.com/alexberardi/jarvis-server

go 1.27.1

// The admin SPA's npm dependencies are not Go packages; some ship stray .go files.
ignore ./web/admin/node_modules

require (
	github.com/dlclark/regexp2 v1.12.0
	github.com/ebitengine/purego v0.11.1
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/emiago/diago v0.41.0
	github.com/emiago/sipgo v1.6.0
	github.com/godbus/dbus/v5 v5.2.4
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/gorilla/websocket v1.5.3
	github.com/icholy/digest v1.1.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/jezek/xgb v1.3.1
	github.com/libp2p/zeroconf/v2 v2.2.0
	github.com/makiuchi-d/gozxing v0.1.1
	github.com/mochi-mqtt/server/v2 v2.7.9
	github.com/pion/rtp v1.10.6
	github.com/pressly/goose/v3 v3.26.0
	github.com/warthog618/go-gpiocdev v0.9.2
	github.com/xuri/excelize/v2 v2.11.0
	github.com/zaf/g711 v1.4.0
	golang.org/x/crypto v0.53.0
	golang.org/x/image v0.46.0
	golang.org/x/net v0.56.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.60.1
	rsc.io/qr v0.2.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/emiago/dtls/v3 v3.0.0-20260122183559-8b8d23e359c0 // indirect
	github.com/go-audio/riff v1.0.0 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/miekg/dns v1.1.43 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pion/dtls/v3 v3.1.5 // indirect
	github.com/pion/ice/v4 v4.3.0 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.16 // indirect
	github.com/pion/sdp/v3 v3.0.18 // indirect
	github.com/pion/srtp/v3 v3.0.10 // indirect
	github.com/pion/stun/v3 v3.1.6 // indirect
	github.com/pion/transport/v4 v4.0.2 // indirect
	github.com/pion/turn/v5 v5.0.12 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/richardlehane/mscfb v1.0.7 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/rs/xid v1.4.0 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	gopkg.in/hraban/opus.v2 v2.0.0-20230925203106-0188a62cb302 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// diago v0.41.0 requires sipgo v1.6.0, a tag that was moved after the Go proxy cached it: the
// proxy's v1.6.0 is not GitHub's. Pin the last unambiguous release instead; the require line
// above still says v1.6.0, this replace is what builds. docs/decisions/2026-10-22-sip-library-pin.md
replace github.com/emiago/sipgo => github.com/emiago/sipgo v1.5.0

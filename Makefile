# Convenience targets. Go itself needs none of these: `go build ./...` works without Node and
# embeds the admin placeholder page instead of the UI.

GO ?= go

.PHONY: admin jarvisd

# Build the admin SPA into web/admin/dist/ui, which the next jarvisd build embeds.
admin:
	npm --prefix web/admin ci
	npm --prefix web/admin run build

# A release-like jarvisd with the admin UI embedded.
jarvisd: admin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o jarvisd ./cmd/jarvisd
	$(GO) test ./web/admin -run TestEmbeddedUI -tags release_ui -count=1

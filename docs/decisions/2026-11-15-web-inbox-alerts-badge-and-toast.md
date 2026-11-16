# Web inbox alerts are a live badge and a toast, not Web Push (v1)

- **Date:** 2026-11-15, user.

While a Jarvis tab is open, the web app shows new inbox items as a live unread badge and an
in-page toast. It does not use browser Web Push in v1. Web Push needs HTTPS or localhost, a
service worker and stored subscriptions, and most self-hosted installs are plain-HTTP LAN
addresses where it can't work. The phone app already alerts when no tab is open.

Plan: [webapp 03](../webapp/03-inbox-parity.md).

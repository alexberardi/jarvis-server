# Device integrations run on desktop agents too

- **Date:** 2026-10-26 (user: "devices working on the agent are a mandatory feature")

A Pantry package's device families and managers (`device_protocol`, `device_manager`) now load on
a desktop agent as they do on a Pi: listed for the sidecar, answering the app's device scan, list
and state requests and OAuth's auth-ready, and reported in the heartbeat's protocols. A
device-only package (Govee, Hue, Nest, HomeKit, …) installs on a computer instead of being
refused with "has no commands a computer can run".

The rule for what a computer refuses is the user's: **anything that needs system setup**
(`apt_packages`, `apt_sources`, `post_install`) stays Pi-only, as does a `targets` list without
`desktop`. The app's store (`computerCanRun`) and jarvisd's desktop-install check already apply
that rule, so only the agent changes.

Still Pi-only: a package's agents (`pkgagents`, e.g. comfyui_jobs) and bluetooth. Supersedes the
"device protocols … are skipped" part of
[2026-10-15-pantry-packages-on-desktop-agents](2026-10-15-pantry-packages-on-desktop-agents.md).

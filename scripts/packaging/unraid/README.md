# Unraid template

Community Applications (CA) template for the jarvisd container image.

- `jarvis.xml`: the CA template. It installs `ghcr.io/alexberardi/jarvisd:latest` with
  bridge networking, the three ports (API 7700, admin 7710, MQTT 1884), the appdata path
  `/mnt/user/appdata/jarvis` and the NVIDIA variables — nothing to fill in.
- `jarvis.png`: the icon CA shows in the app store (a copy of the agent's tray icon).
- `template_test.go`: keeps the template in line with the image name, ports and paths.

Submitting the template to Community Applications is the maintainer's step: point CA at
`https://raw.githubusercontent.com/alexberardi/jarvis-server/main/scripts/packaging/unraid/jarvis.xml`.

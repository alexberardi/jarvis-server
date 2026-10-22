# Unraid template

Community Applications (CA) template for the jarvisd container image.

- `jarvis.xml`: the CA template. It installs `ghcr.io/alexberardi/jarvisd:latest` with
  bridge networking, the three ports (API 7700, admin 7710, MQTT 1884), the appdata path
  `/mnt/user/appdata/jarvis` and the NVIDIA variables — nothing to fill in.
- `jarvis.png`: the icon CA shows in the app store (a copy of the agent's tray icon, 128x128).
- `ca_profile.xml`: the maintainer profile CA shows on the author's page (optional for CA).
- `template_test.go`: keeps the template in line with the image name, ports and paths, and
  checks what CA's review looks at (https links, category format, the profile parses).

## The template repo

CA reads templates from a GitHub repo you register with it, and it parses every `.xml` file in
that repo. jarvis-server also tracks a non-template XML
(`internal/agent/deploy/windows/net.jarvisautomation.agent.xml`), so CA gets a **dedicated
repo**, `alexberardi/unraid-templates` (the usual CA convention), holding copies of two files at
its root:

```
unraid-templates/
├── ca_profile.xml   # copy of scripts/packaging/unraid/ca_profile.xml
└── jarvis.xml       # copy of scripts/packaging/unraid/jarvis.xml
```

This directory stays the source of truth. The template's `TemplateURL` and `Icon` keep pointing
at the raw files on jarvis-server's `main`, so an installed template picks up changes merged here
without the copy; re-copy `jarvis.xml` into the template repo whenever it changes so new
installs from CA get it too.

## Submitting to Community Applications (maintainer only)

Only the maintainer can do these; nothing in this repo does them. The forum and CA links were
not verified from the build environment: check them at submission time.

1. **Wait for `:latest`.** The template installs `ghcr.io/alexberardi/jarvisd:latest`, which
   only moves on a release tag (rc19 or later). On an Unraid box, `docker pull
   ghcr.io/alexberardi/jarvisd:latest`, install from the raw `jarvis.xml` URL (Docker tab →
   Add Container → Template, or a test template in `/boot/config/plugins/dockerMan/templates-user/`)
   and run the setup wizard once.
2. **Create an Unraid forum account** at forums.unraid.net.
3. **Open the support thread** in the forum's Docker Containers support subforum, titled
   `[Support] Jarvis` (CA moderators require one thread per container): what it is, the
   template's ports and paths, the NVIDIA steps, and a link to GitHub issues.
4. **Put the thread URL in `<Support>`** in `jarvis.xml` (it points at GitHub issues until the
   thread exists), merge that here, and keep `go test ./scripts/packaging/unraid/` green.
5. **Create `alexberardi/unraid-templates`** (public) and push the two files above to its
   `main`.
6. **Check the category** against CA's current list at submission (the template uses
   `HomeAutomation: Productivity:`; switch if CA has a better fit, e.g. an AI category).
7. **Submit the template repo** through the submission form linked from the Community
   Applications support thread on the forum, giving `https://github.com/alexberardi/unraid-templates`.
8. **Answer the moderators' review** in the forum; once accepted, Jarvis appears in the Apps tab
   (CA's feed refreshes every few hours).

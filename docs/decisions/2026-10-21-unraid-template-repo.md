# Community Applications reads the Unraid template from a dedicated repo

- **Date:** 2026-10-21 (build agent, item #421; for the user to confirm in review)

Community Applications parses every `.xml` file in a template repo it is given. jarvis-server
also tracks a non-template XML (`internal/agent/deploy/windows/net.jarvisautomation.agent.xml`),
so CA is pointed at a dedicated repo, `alexberardi/unraid-templates` (the usual CA convention),
that holds copies of `jarvis.xml` and `ca_profile.xml` at its root.

`scripts/packaging/unraid/` stays the source of truth: the template's `TemplateURL` and `Icon`
keep pointing at the raw files on jarvis-server's `main`, so installed templates update from
here and the icon is not copied. The template repo only has to be re-synced when `jarvis.xml`
changes, for new installs from CA. The submission steps are in
`scripts/packaging/unraid/README.md`.

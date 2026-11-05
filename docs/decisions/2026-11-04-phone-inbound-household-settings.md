# Inbound phone setup through household settings

- **Date:** 2026-11-04; user requested household inbound setup for the SIP demo.

Expose all six `phone.inbound.*` settings as household overrides in the superuser Settings
page. The existing household editor also needs to create the first override: select a
household, enter its value, then save. Editing and removing overrides use the same existing
admin API. This completes the missing UI path recorded in
[the inbound-default decision](2026-10-26-phone-inbound-default.md).

Keep the mobile household allowlist unchanged. A household's ability to answer a shared SIP
account affects other households, so selecting the owner remains an install administrator's
job. `Definition.Household` advertises scoped admin editing; it does not grant household users
access. Tests enumerate the six admin-only keys separately from the mobile allowlist.

Keep the effective-value behavior: a household's own value wins over the install default,
and multiple enabled households resolving to one SIP account cause calls to be refused.
For a shared line, the setup guide leaves the default off and enables the intended household.
The existing SIP routing, limits, validation and call handling stay in place.

Use a disposable mobile emulator for the demo's inbox, approval and result screens. The
admin configures the line; mobile already renders the call plan's editable fields and actions.
The [walkthrough](../dev/sip-demo-walkthrough.md) demonstrates both against the same sandbox.

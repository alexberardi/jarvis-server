# Phone: the install's default turns inbound calls on

- **Date:** 2026-10-26 (user: "i'd rather clean this up and simplify it", after writing the setup
  guide for a new user)

**Before.** An inbound call belonged only to a household whose *own* `phone.inbound.enabled` was
true ([2026-10-22-phone-inbound-receptionist.md](2026-10-22-phone-inbound-receptionist.md)). No UI
could create that value in rc18.7: the admin's Settings page edits a household value only for
keys on the mobile allowlist, and the mobile app's household settings don't list inbound. The
setup guide needed two `curl` commands for the one switch.

**Now.** `inboundHousehold` checks every household (those with their own value plus every
household auth knows), each by its effective value: its own, else the install's default. So on a
one-household install, turning the default on in **Settings → Command center → phone_calls** is
enough. The rest is unchanged: the household's SIP account must be the line's, `phone_calls.enabled`
must be on, and two households sharing one account with inbound on get no calls (480 and an error
in the log). On a multi-household install with a shared system account, the default alone is
therefore ambiguous, and a household opts out with its own false.

The transfer targets and the inbound limits already fell back to the default (`Settings.Get` with
the household scope), so the guide needs no commands at all.

**Not done.** Letting the admin add a household's own value for any household-scoped key (a
multi-household install with one account per household still needs it per household).

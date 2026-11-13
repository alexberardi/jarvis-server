# The Discord bridge is a cc sub-package, secrets come through the app

- **Date:** 2026-11-13 (user; design in [discord-bridge.md](../discord-bridge.md))

People reach Jarvis remotely through a Discord bot, with no VPN. The bot is `internal/modules/cc/discord`,
wired by `internal/modules/cc/discord_wire.go`, as the phone calls are (`cc/phone`).

- **Why a cc sub-package, not a new module.** It runs chat turns as a household user through the
  mobile chat pipeline (`cc.Module.ChatText`), reads household settings, and needs the cc database
  and speech services. A separate module would reach into all of that.
- **Secrets only through the app (user: the author wants tokens and keys entered in the mobile app).**
  The bot token, server and channel IDs are household settings (`discord.*`), set by a household admin
  over the existing household settings routes, token write only and masked, like the Twilio
  credentials. No environment variable or file. The settings store does not encrypt at rest; the
  bridge does not claim it does.
- **Library: `disgo` with `dave-go`, not `discordgo` (user approved the swap).** Discord now requires
  end-to-end encrypted (DAVE) voice for non-stage calls. `dave-go` is pure Go, so jarvisd keeps
  `CGO_ENABLED=0`. The only DAVE option for `discordgo` found was a fork wrapping the C library
  `libdave`, which would need cgo. `dave-go` is v0.5, self-described as young, with no independent
  audit; the risk is stated in the guide, and text, clips and photos do not depend on it. A spike
  (2026-11-13) joined an encrypted channel, received a linked user's audio, and played a tone back.
- **Opus without cgo.** libopus is loaded at run time with purego (`cc/discord/opusnative`), the same
  way the speech libraries are. The container image gains `libopus0`. Without it only the voice
  features say what is missing.
- **Identity.** A Discord account is linked to a Jarvis user per household by a one-time code made in
  the app (`cc_discord_links`). Unlinked accounts are ignored silently. A linked account acts as that
  user, so the guide tells people to protect it.
- **Jarvis never starts a conversation.** It replies only to what it was sent, in the place it was
  sent. The one exception is a reminder pushed by a node (`/node/push-notification`, category
  `reminder`, `alarm` or `timer`), sent as a direct message to the user it is for, only if they opted
  in (off by default). It follows the push: a reminder the attention gate withholds is not sent either.
- **One bot per household,** one text channel and one voice channel. The bot joins the voice channel only
  when a linked person in it runs `/jarvis join`, hears linked people only, records nothing, and leaves
  when alone for a minute.

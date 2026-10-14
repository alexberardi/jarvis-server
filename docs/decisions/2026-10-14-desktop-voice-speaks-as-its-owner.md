# A desktop agent's voice turns speak as its signed-in member

- **Date:** 2026-10-14 (user)

A voice turn from a desktop agent (`node_kind: desktop`) is treated as coming from the member
signed in to it (`cc_nodes.owner_user_id`: who approved its pairing code or signed in on its
first-run page, [device sign-out](2026-10-14-device-sign-out.md)), the way mobile and web chat
treat the JWT user as the speaker. Desktop nodes get no voice enrollment and no voiceprint
matching.

- **Who:** the owner, while that member is still in the node's household. The membership comes
  from auth on every request, so an owner removed from the household stops counting at once,
  mid-conversation included. No owner (an admin-key node, or one paired before migration 00180)
  or an owner who left means today's behaviour: the speaker is unknown until a voice match, and
  per-user things refuse (D21). Never a guess.
- **Wording without an owner (2026-10-14, user):** on a desktop agent with no usable owner the
  fix is signing in again, not a voice profile. Per-user refusals, `identify_speaker` and the
  speaker block say "I don't know who's signed in on this computer. Sign in again from the
  Jarvis Automation menu (Set up / Settings…)." (`servertools.SignInAgain`,
  `prompts.SignedOutComputerBlock`), ahead of the "recognition is off" wording, and the memory
  tools are offered there so that they can say it. Pi nodes keep their wording. Scenarios:
  `tools/pipeline/scenarios/desktop_owner_removed.json` (`"owner": "removed"`).
- **What it changes:** the prompt names the owner (display name, else username), and memory,
  personal reminders, personal secrets and transcripts use the owner, as chat does. The memory
  tools are offered even with speaker recognition off. The transcription runs no speaker pass
  for those turns (no embedding, so it costs no latency); its answer names the owner
  (`speaker.user_id`, confidence 1), which the agent passes to its own tools as mobile STT
  reports the JWT user. A voice match or a dev speaker hint never overrides the owner. No voice-presence signal is written: nobody's voice was recognised,
  and a laptop may be away from home.
- **Chat keeps its user:** a web or mobile chat aimed at someone else's desktop agent still acts
  as the chat's JWT user, never the agent's owner.
- **Privacy:** anyone talking to that computer is treated as its signed-in user, which is what a
  personal computer means. A household that wants it otherwise signs the agent out (Add-ons, or
  the status icon's "Sign out of Jarvis Automation").

This makes an exception to D21's "no fallback to the node owner" ([cc/06 §0](../cc/06-media-and-voice-identity.md))
and to D3's "nothing is keyed per node", for desktop agents only. Pi nodes are unchanged. Code:
`internal/modules/cc/device_owner.go`; pipeline scenarios `tools/pipeline/scenarios/desktop_owner.json`.

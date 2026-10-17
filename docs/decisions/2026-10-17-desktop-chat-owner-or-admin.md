# Desktop agents: chat and actions are the owner's or a household admin's

- **Date:** 2026-10-17 (user, item #102)

A desktop agent's client tools read the screen and the browser page and type on someone's own
computer, so `POST /mobile/chat`, `/mobile/chat/warmup` and `POST /nodes/{id}/actions` on a
desktop agent (`node_kind=desktop` or `install_mode=desktop-agent`) are allowed only to its owner
(`owner_user_id`, while still a member of its household) or a household admin; other members get
a 403. A desktop without a usable owner (made with the admin key, paired before owners were
recorded, or whose owner left) is admin only, the same rule as sign-out (`canSignOut`). Pis stay
open to every member of their household: a shared kitchen speaker has no such risk. Chat to a
node in another household is still a 404.

# How the admin's workflow editor maps onto cc's workflow API

- **Date:** 2026-10-19 (build agent, plan P3 piece 27; held for the user's review)

The Workflows and Recordings pages (admin SPA) edit what
[2026-10-19-workflow-and-recording-access.md](2026-10-19-workflow-and-recording-access.md) stores.
Where the plan's page and the API didn't line up:

- **"Which of these change each time?"** has no separate candidate store: the candidates are the
  workflow's params (on) and the literal values its steps type or choose (off). Turning a param
  off saves its example back into the steps; turning a typed value on saves it as a `text`,
  `date`, `month` or `number` param (guessed from the value, editable) and the step as `{name}`.
  Renaming a param renames `{old}` in the steps' text and intents.
- **"Asks first"** comes from a step's `risk`. cc accepts only computeruse's risk categories, so
  making another step ask means picking which one applies (the editor offers them in plain
  words, "Sends a form to someone" first). A step that was risky when loaded stays risky in the
  editor: the user can't switch off a confirmation the risk classifier asked for.
- **An added step** (intent + browser or desktop) has no recorded element, so its action is a
  `screenshot` with that intent: replay (piece 31) resolves it from the intent like any step
  whose element it can't find.
- **Last run** reads "never" until replay records runs; **Run** calls the route and says it isn't
  available yet while the route answers 501.
- **Owner** is "Yours" or "Another member's": members have no household member list in the
  admin.
- **Keyframes** stay on the computer that recorded them (no image route): the page shows how many.
- **Retention** (`computer.recording_retention_days`) joins cc's household-controllable allowlist,
  so a household admin sets it on the Recordings page (any member reads it). The household
  settings PUT now runs a declared validator on the value as stored (`int64`), so its 1 to 3650
  bound is a 400 there.
- **Recordings list** carries `steps` (the trace's event count); the list leaves out the trace.

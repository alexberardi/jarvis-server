# An optional cloud model slot, and a slot choice per feature

- **Date:** 2026-10-22 (user asked for it; build agent, loop #488)

The user (2026-10-22): "split off a new model.cloud option that is optional and then you can
select which model you want to use for a given task. so for computer control you could run off
the live, background, or cloud."

Before this, the llm module had two chat slots, `live` and `background` (LD1). Either could
be a remote endpoint, but only by giving up a local slot, and a computer task reached a cloud
model only through `Task.UseCloud`, which took whichever of the two was remote. No code set
`UseCloud`.

**The `cloud` label.** A third LLM label, `llm.cloud`, in `engine.LabelDefs`, marked
`RemoteOnly`. It has the remote fields only: `remote_url`, `remote_api` (the provider's
dialect: auto, openai, anthropic), `remote_model`, `remote_api_key` (secret), `remote_vision`,
`context` (the window, for compaction) and `fold_system_messages`, plus `engine`, which takes
`remote` (the default) or `off`. It has no model, GPU or local-engine fields. Every field is
empty by default, so an install sends nothing anywhere until a superuser fills in a URL. An
unset URL resolves to `ErrNotConfigured`, like an unset remote on the other labels. It is
served by the same path as the existing remote endpoints (`describe` in the resolver): no
engine, no warm-up, no GPU placement, and nothing to assign from the model catalog (Install
and Assign refuse it).

**Only features that name it reach it.** The public API's model names still map to live or
background (`NormalizeLabel`, `echoLabel`), and so does a node's `/node/llm/chat` `model`, so no
HTTP caller can pick `cloud`. Every ChatRequest whose label comes from a request or a setting
goes through `NormalizeLabel`; only code that names `llm.LabelCloud` (computer control's
`pickLabel`) can set it. Inside jarvisd, `ChatRequest{Label: "cloud"}` goes to the cloud label (`chatLabel`). `/v1/models` and the health
response's `slots` keep listing only live and background: they describe the public API, which
cannot reach cloud. The Models page's label list (`GET /models/labels`, the resolver's Status)
shows it, as `not_configured` until it is set up.

**A slot choice per feature (the reusable pattern).** A feature that may run on any slot has
one string setting whose values are `llm.SlotChoices`: `auto`, `live`, `background`, `cloud`.
`llm.SlotSetting(key, category, description)` builds its definition (options and a validator),
and `auto` means the feature's own default, which never picks `cloud`: the household's data
leaves the network only when someone chose that. Computer control is the first:
`computer.model` (system-wide, superuser; not household-controllable). The planner or summaries
add theirs later the same way. On the admin Models page, a "Feature models" card lists each
such setting with a slot picker; choosing cloud shows what the feature sends to the provider
(for computer control: "page contents and screenshots of your browser are sent to <provider>
while a task runs").

**`computer.model=cloud`** takes the cloud label, or fails the task with `ErrNoCloud` ("No cloud
model is set up") when it isn't configured. `Task.UseCloud` is removed: the setting replaces
it, and nothing set it. This supersedes the cloud part of
[2026-10-19-computer-task-model.md](2026-10-19-computer-task-model.md) ("no third model slot";
a per-task opt-in through a remote label).

**Not done here.** A per-household choice (a household admin picking cloud for the household's
data) would mean adding `computer.model` to cc's household-settings allowlist, which is an
access change; it stays a superuser setting until someone asks.

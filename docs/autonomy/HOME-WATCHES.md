# Home watches and rules (P4)

Status: draft, questions open. Brainstormed 2026-11-23.

## Intent

Jarvis notices when something is off at home and either tells you or fixes it. Example:

> **When** the garage is open **and** no one is home **and** the heat is on, **then** turn off
> the heat.

A rule is written by a person as a short list of conditions plus a result, in the style of
routines' simple mode. It is not a programming language.

## What exists

- Watches (`internal/modules/cc/watches*`): one condition (`lt`, `gt`, `equals`, `contains`,
  `changed`, `absent_for`) on one signal kind, with a facts filter. Created by voice. They notify;
  they don't act.
- Signal automations (`signals.go`): a free-text instruction per catalog kind (`presence.*`,
  `appt.upcoming`). On the signal, one background-LLM tool call picks the action, then runs it or
  posts a confirm card.
- Presence signals from the phone. Home device state is **not** published as signals yet: that's
  the `device.state` producer that PLAN P4 / A6 describes.

## Decided

- **Q1 (2026-11-23): several conditions, all of which must hold, then a result.** Written by a
  person like routines' simple mode ("condition 1: garage open, condition 2: no one home,
  condition 3: heat is on → turn off the heat"). This replaces the single-condition-plus-guard
  idea.
- **Q2 (2026-11-23): translate when the rule is saved; the translation is a draft the person
  confirms.** Each line shows the device and state the model picked as an editable row. A line goes
  to the device picker, with the guess filled in, when the model says unclear or a check fails:
  the value isn't one the device can have; a `gt`/`lt` threshold isn't a number the person wrote;
  or the device step finds no candidates or several ("which one?" with those candidates). The
  translation is two model calls (candidates, then the check; see Evidence), not one call plus a
  yes/no check. After saving, checking a rule needs no model. Device groups ("any window") and
  days of the week join the vocabulary.
- **Q3 (2026-11-23): three features on one engine.** Rules, watches and signal automations stay
  separate features to the person, and all three run on one shared condition/trigger engine.
- **Q4 (2026-11-23): a rule fires once when all its conditions become true**, and can fire again
  only after at least one condition has been false. Any condition can carry "for N minutes"
  (the garage open for 10 minutes), reusing watches' `absent_for` timer.
- **Q5 (2026-11-23): results are translated when the rule is saved**, with the same two steps as
  conditions, and run as stored when the rule fires (no model at fire time). A result line with
  several candidate devices is a **multi-picker with all of them ticked** ("turn off the heat" →
  all three thermostats); the action runs on each ticked device. A rule may have several results,
  and "tell me" is one. Following from this, a condition with several candidates offers **any /
  all / just these** ("any window is open"), which covers device groups.
- **Q6 (2026-11-23): each rule chooses: run on its own, or ask first with a card.** Default: run
  on its own, except results that open up the house (unlock, open the garage, disarm the alarm),
  which default to ask first. Every run on its own posts an inbox line with what ran and why.
- **Q7 (2026-11-23): household admins only** create, edit and delete rules, as with signal
  automations. Watches stay per member.
- **Q8 (2026-11-23): rules are written on the web and in the mobile app; both are built.** Web
  first, then mobile (order left to us). No creating rules by voice in v1: the confirm step needs
  a screen.
- **Q9 (2026-11-23): only the devices in use publish `device.state`.** jarvisd sends each node the
  entities that an active rule or watch refers to. The SDK's device manager gets an optional push
  hook (Home Assistant: its websocket's `subscribe_entities`). Managers without it are polled by
  the node with `get_state` every 30 s, for the listed entities only, and such conditions show
  "checked every 30 s".
- **Q10 (2026-11-23): a failed action is retried once after a minute,** then posts an inbox card
  naming the device, the reason and the rule, with a Retry button. No further retries; the rule
  counts as fired. **Each rule's page shows its recent firings**: when, every condition's value at
  that moment, and each action's outcome (done, failed, waiting for a tap). Kept 30 days.

## Evidence: translating plain-words conditions (2026-11-23)

63 condition lines against the Home Assistant demo's devices (125 entities: 3 thermostats,
5 vacuums, oddly named locks) and 3 household members, on Qwen 3.5 9B (devbox), temperature 0,
JSON-schema output, one call per line, ~0.5 s each.

| Category | Lines | Acceptable | Confidently wrong |
| --- | --- | --- | --- |
| Plain ("garage open", "Sam is home", "after 10pm") | 36 | 36 | 0 |
| Vague ("the house is empty", "it's dark out") | 15 | 14 | 1 ("power use is really high" → `> 0`) |
| Ambiguous ("heat is on" with 3 thermostats) | 6 | 5 | 1 ("inside temperature" → the outside sensor) |
| Not possible ("the dog is outside", "garage light" with none) | 7 | 4 said unclear | 3 |

The misses that matter are **silent substitutions**: "the garage light" → Kitchen Lights, "any
window" → Kitchen Window, "inside temperature" → Outside Temperature. With several equally good
devices the model also picks one silently instead of saying unclear.

A second yes/no call ("is this picked device clearly what they named?") caught 3 of the 4 device
misses, but it also sent 12 of 44 correct answers to the picker. Some of those are welcome
(3 thermostats, 5 vacuums) and some are noise ("ceiling fan on" → Ceiling Fan).

Two of the misses are product gaps rather than model errors: "any window is open" (a group) and
"on a weekday" (the model invented a `day_of_week` field).

**Better prompting fixes it: two steps (same day, same 63 lines, same model).**

1. Call 1 names the thing the line is about ("garage light") and lists every candidate device,
   with the rule that another room or another kind of thing is not a candidate. Few-shot
   examples use a different home's devices.
2. Code decides: no candidates, or `kind: none` → unclear; several → "which one?" with exactly
   those candidates; one → call 2 (the original prompt, told which device) writes the check. If
   call 2 names another device, or a `gt`/`lt` line has no number in it → unclear.

| | One call | Two steps |
| --- | --- | --- |
| Right | 54 | 47 |
| Unclear, correctly (goes to the picker) | 4 | 14 |
| Unclear, but it could have answered | 0 | 1 ("it's dark out": the Sun entity isn't named as daylight) |
| Confidently wrong | 5 | 1 ("on a weekday": days aren't in the vocabulary yet) |

Plain lines stay 36/36. Every silent substitution is gone ("garage light" and "inside
temperature" now have no candidates). Each ambiguous line comes back with its real candidate
list, which is the "which one?" picker. Two runs gave identical grades; about 1 s per line.
Putting all of this in one call with a longer schema failed: the model filled `named` and
`candidates` and then left the check fields empty, because jarvisd validates the schema but
doesn't constrain generation to it. So the second yes/no check from Q2 isn't needed.

## Open questions

None for v1. Smaller choices (names, exact copy, card layout) are settled in the slices' PRs.

## Build plan

Each slice lands on its own and is usable or testable alone.

1. **Engine (jarvisd, `internal/modules/cc`).** Rule store (rules, conditions, results, delivery,
   enabled, admin-only routes with tier expectations), evaluation as a `*` signal reaction, firing
   once when all conditions become true (Q4), "for N minutes" on the scheduler's `KindOnce`
   triggers like `absent_for`, presence and time/day conditions, actions through the existing
   node tool dispatch, retry once then a card (Q10), firing history kept 30 days. Tested with
   signals posted directly; no device producer needed yet.
2. **Translation.** The two-step prompts on the background model behind one "translate this
   draft" route: rows with the chosen check or the candidates, any/all/just these for conditions,
   a multi-picker with everything ticked for results. The 63-line eval becomes a checked-in
   harness so prompt changes are measured.
3. **`device.state` producer.** jarvisd sends each node its watched entities. SDK: an optional
   push hook on the device manager plus the agent's 30 s poll fallback. Home Assistant package:
   push via `subscribe_entities`. Catalog entry for `device.state`. SDK release, HA package
   release, Pantry redeploy.
4. **Web Rules page** next to Routines, in the routines editor's style. Design audit and
   screenshots per `web/admin/CLAUDE.md`; `e2e/coverage.json` entry.
5. **Mobile Rules screen** on main (the IA redesign has landed), one PR since each merge ships a
   build.
6. **One engine for all three (Q3).** Move signal automations and watches' signal source onto the
   rule engine's conditions; their screens and behaviour stay as they are.

Hand-test at the end on the HA demo: "garage open for 2 minutes + no one home + heat on → turn
off the heat" on all three thermostats, done from the web and from the phone.

# Call follow-ups: proposable actions offered on the Call finished card

After a call, the **Call finished** card can carry buttons that installed commands offer: "Add to
Calendar · Wed Nov 11, 1:30 PM", "Remind me the day before". They are **proposable actions**
(SDK `ProposableAction`, doc 10's tap-to-confirm cards) that opt in to being offered after calls.
After the call, the background model reads the transcript, picks the ones that apply and fills in
their params; the server checks every pick; the chosen ones become the card's buttons, and a tap
goes through the existing proposable-action dispatcher. Nothing runs without a tap.

Decision: [../decisions/2026-11-08-call-followups.md](../decisions/2026-11-08-call-followups.md).
Proposals: [10-signals-attention-proposals.md](10-signals-attention-proposals.md). Phone:
[11-phone.md](11-phone.md).

## 1. What a command author writes

The proposable action they would write anyway, plus one sentence saying when it fits a call:

```python
from jarvis_command_sdk import ProposableAction, JarvisParameter, callback, CommandResponse

class CalendarCommand(IJarvisCommand):

    @property
    def proposable_actions(self) -> list[ProposableAction]:
        return [ProposableAction(
            callback="add_event",
            params=[
                JarvisParameter("title", "string", required=True),
                JarvisParameter("starts_at", "datetime", required=True),
                JarvisParameter("duration_minutes", "int"),
                JarvisParameter("location", "string"),
            ],
            card_title="Add to your calendar?",
            confirm_label="Add to Calendar",
            idempotency_param="title",
            offer_after_calls="the call booked an appointment or reservation for a specific date and time",
        )]

    @callback("add_event")
    def add_event(self, data: dict, request_info) -> CommandResponse:
        ...
```

- **New:** `offer_after_calls: str | None` on `ProposableAction` (≤ 200 chars). Set, the action may
  be offered after a call when that sentence fits; unset (the default), it never is.
  Everything else is the existing contract: typed params, labels, blast tier, idempotency.
- Only commands whose **Share with phone calls** switch is on are considered (the switch that
  already gates call-plan context).
- `jdt test` gains a sample call transcript, and a check that every action with
  `offer_after_calls` names a real `@callback` and has typed params.

## 2. After the call

```
call ends ─▶ assess (background model): summary, facts, goal_achieved           (today)
          ─▶ gather: every live node's report_tools (nodeTools, cached 30 s): the proposable
             actions with offer_after_calls, from commands with Share with phone calls on
          ─▶ pick (background model): which apply, with params
          ─▶ check (server, §3)
          ─▶ post the Call finished card with ≤ 3 confirm buttons
```

- **Pick** is one background-model call with the transcript, the assessment and the candidate
  actions as tools (name, `offer_after_calls`, params). It may call none, or each at most once. It
  runs only when there is a transcript and at least one candidate.
- **The card waits** for the pick (budget 90 s; past it, the card posts without buttons and a late
  pick is dropped). A call with no candidates posts exactly as today.

## 3. The server's check

Each pick is dropped unless:

1. It names a gathered action and only its declared params, each of the declared type, with every
   required param present (the dispatcher's `validateAgainstParams`, run early).
2. **Every date and datetime is grounded**: it resolves (timewindow's `ParseProposed` and
   `ResolveRelativeDays`) from a date or time said in the call's transcript, in the household's
   time zone, and lies in the future. A time nobody said is a dropped button, never a guess.
3. No string carries one of the call's **Only if they ask** details (`live.FindRestricted`).

At most 3 survive, in the model's order. A button's label is the action's `confirm_label` plus the
key param in brief ("Add to Calendar · Wed Nov 11, 1:30 PM").

## 4. The buttons and the tap

The chosen picks become the card's `interactive_elements` exactly as `emitProposalCard` builds a
confirm element (`command: jarvis.proposable_action`, `callback: execute`, `target: server`,
`_action` = target command, callback, node, idempotency key, blast tier, plus the params), with
idempotency key `call:<session>:<n>`. No dismiss or "never suggest" button: ignoring is
dismissing, and the card is about the call.

A tap runs the existing dispatcher (`proposalExecute`): the node must be in the caller's
household, the command must still advertise the action as proposable, the params are validated
against its declaration again, and the idempotency key runs it once. Its result card ("Added to
your calendar", or "Couldn't run that: no device is available") is the existing behaviour.

**The household gate (open, Q1).** The dispatcher refuses every proposal while `proposals.enabled`
is off, its default, so as written no follow-up would ever run. Proposed: the gate also passes for
an idempotency key the server issued as a call follow-up for that household (kept with the call's
session and its retention), so a household that turned on phone calls gets follow-ups without
turning on agent proposals in general. A key can't be forged into another household's, and a
member can only reach actions the command declared proposable.

## 5. Build order

1. **SDK**: `offer_after_calls`, its validation, the `jdt test` fixture.
2. **Server**: gather, pick, check, the issued-key gate (Q1), the card's elements.
3. **Calendar package**: `add_event` declared proposable with `offer_after_calls`.

No new node code: nodes already report proposable actions through `report_tools`, and older nodes
simply report none with the new field.

## 6. Later

- **Editable params** (`ProposableAction.editable`): fix a mis-heard time on the card before
  confirming. The SDK has the field; the card does not show it yet.
- Any background task that ends in a summary card (errands, deep research) can offer actions the
  same way, with its own `offer_after_*` sentence. Phone first.

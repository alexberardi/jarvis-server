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
- **Not gated by Share with phone calls** (user, 2026-11-08). That switch keeps a command's data
  out of calls; a follow-up sends call details into the user's own command, only on a tap. A
  household that turned calendar sharing off still wants "Add to Calendar".
- `jdt test` gains a sample call transcript, and a check that every action with
  `offer_after_calls` names a real `@callback` and has typed params.

## 2. After the call

```
call ends ─▶ assess (background model): summary, facts, goal_achieved           (today)
          ─▶ gather: every live node's report_tools (nodeTools, cached 30 s): the proposable
             actions with offer_after_calls
          ─▶ pick (background model): which apply, with params
          ─▶ check (server, §3)
          ─▶ post the Call finished card with ≤ 3 confirm buttons
```

- **Pick** is one background-model call with the transcript, the assessment and the candidate
  actions as tools (name, `offer_after_calls`, params). It may call none, or each at most once. It
  runs only when there is a transcript and at least one candidate.
- **The card waits** for the pick (budget 2 minutes, as the assessment; past it, the card posts
  without buttons). A call with no candidates posts exactly as today. On prod's 31B a pick took
  5-45 s, once 94 s.
- The action's `idempotency_param` is never shown to the model: the server fills it with the
  follow-up's key.

## 3. The server's check

Each pick is dropped unless:

1. It names a gathered action and only its declared params, each of the declared type, with every
   required param present (the dispatcher's `validateAgainstParams`, run early).
2. **Every date and datetime is grounded**: a line of the call (either side) names it, read as the
   live call reads offers (`ResolveRelativeDays`, `spokenClock`, `ParseProposed`: a weekday or
   "tomorrow" plus a time, in the household's zone); or, for a reminder, it is up to 48 hours
   before such a time ("the evening before"). It lies in the future. A time nobody said is a
   dropped button, never a guess.
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

**The household gate (decided, user 2026-11-08).** The dispatcher refuses every proposal while
`proposals.enabled` is off, its default, so as written no follow-up would ever run. Decided: the gate also passes for
an idempotency key the server issued as a call follow-up for that household (kept with the call's
session and its retention), so a household that turned on phone calls gets follow-ups without
turning on agent proposals in general. A key can't be forged into another household's, and a
member can only reach actions the command declared proposable.

## 5. The first follow-ups (user, 2026-11-08)

| Follow-up | Command | What exists | Work |
|---|---|---|---|
| Add to Calendar | Calendar package, `create_event` | Proposable (title, start, end, location editable) | `offer_after_calls` |
| Remind me… (the day before an appointment, to call back, to pick up an order) | Built-in reminder, `set_at` | Proposable, absolute time | `offer_after_calls` on the Go side |
| Add to your to-do ("bring your insurance card") | Built-in to-do | Not proposable yet | A proposable add action |
| Draft an email ("email us the photos at …") | Email package | Drafts flow exists | A proposable draft action |

Not a fit: media, weather, sports, news and lookups (nothing to do after a call); locks and
security (an irreversible action from what a stranger said); medication tracker (left out by the
user; the reminder covers a pharmacy pickup).

Save to phonebook was dropped: every completed call already saves or refreshes the business in
the household's phonebook (`upsertContactFromCall`).

## 6. Build order

1. **SDK**: `offer_after_calls` and its validation (jarvis-command-sdk#20).
2. **Server**: gather, pick, check, the issued-key gate, the card's elements.
3. **Agent built-ins**: `offer_after_calls` for reminder `set_at`; to-do's proposable add.
4. **Packages**: calendar `create_event`, email's proposable draft.

No new node code: nodes already report proposable actions through `report_tools`, and older nodes
simply report none with the new field.

## 7. Later

- **Editable params** (`ProposableAction.editable`): fix a mis-heard time on the card before
  confirming. The SDK has the field; the card does not show it yet.
- Any background task that ends in a summary card (errands, deep research) can offer actions the
  same way, with its own `offer_after_*` sentence. Phone first.

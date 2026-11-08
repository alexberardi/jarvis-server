# Call follow-ups: commands offer buttons on the Call finished card

After a call, the **Call finished** card can carry buttons that installed commands offer: "Add to
Calendar · Wed Nov 11, 1:30 PM", "Remind me the day before", "Save to phonebook". A command
declares its follow-ups like tools; after the call, the background model reads the transcript,
picks the ones that apply and fills in their parameters; the server checks every pick before a
button appears; a tap runs the command's `@callback` on its node. Nothing runs without a tap.

Decision: [../decisions/2026-11-08-call-followups.md](../decisions/2026-11-08-call-followups.md).
Phone overview: [11-phone.md](11-phone.md).

## 1. What a command author writes

A follow-up is a declaration plus an ordinary `@callback`. No outcome types to switch on, no
logic deciding when it applies: the `when` sentence is that.

```python
from datetime import datetime
from jarvis_command_sdk import FollowUp, callback, CommandResponse

class CalendarCommand(IJarvisCommand):

    @property
    def call_followups(self) -> list[FollowUp]:
        return [FollowUp(
            callback="add_from_call",
            label="Add to Calendar",
            when="the call booked an appointment or reservation for a specific date and time",
            params={
                "title": str,               # "Brake replacement at Quick Auto Service"
                "starts_at": datetime,      # absolute, the household's time zone
                "duration_minutes": int,
                "location": str,
            },
            required=["title", "starts_at"],
        )]

    @callback("add_from_call")
    def add_from_call(self, data: dict, request_info) -> CommandResponse:
        start = datetime.fromisoformat(data["starts_at"])
        self.create_event(data["title"], start, data.get("duration_minutes", 60), data.get("location"))
        return CommandResponse.success("Added to your calendar")
```

- `FollowUp`: `callback` (a `@callback` on the same command; checked at load, as
  `proposable_actions` are), `label` (≤ 40 chars), `when` (≤ 200 chars), `params` (name → `str`,
  `int`, `float`, `bool`, `date`, `datetime`; ≤ 8), `required` (default: all).
- The callback gets the filled params as `data`, `datetime`s as ISO 8601 with offset. It runs as
  the user the call was for, like any card tap.
- `jdt test` gains a fixture: a sample call transcript and outcome, and a helper that checks a
  command's declarations load and its callback accepts a filled example.

Only commands whose **Share with phone calls** switch is on are asked (the same switch that gates
call-plan context); turning it off removes their follow-ups too.

## 2. After the call

```
call ends ─▶ assess (background model): summary, facts, goal_achieved   (today)
          ─▶ gather follow-ups: every live node in the household, context/query operation "followups"
          ─▶ pick (background model): which follow-ups apply, with params
          ─▶ check (server, §3): drop anything that fails
          ─▶ post the Call finished card with ≤ 3 buttons
```

- **Gather** reuses the call-plan context path (`nodecontext.go`): one `context/query` per live
  node, operation `followups`, 5 s timeout; the node answers from its loaded commands'
  `call_followups`, filtered by Share with phone calls. A node that doesn't answer contributes
  nothing; an older node answers "unknown operation" and contributes nothing.
- **Pick** is one background-model call with the transcript, the assessment and the follow-ups as
  tools (`followup_<n>`, parameters from `params`). The model may call none, or each at most once.
  It runs only when the call produced a transcript and at least one follow-up was gathered.
- **Post** waits for the pick (budget 90 s, then the card posts without buttons; a late pick is
  dropped, not added afterwards). A call with no follow-ups posts exactly as today.

## 3. The server's check

The model's picks are suggestions. Each one is dropped unless:

1. It names a gathered follow-up and only its declared params, each of the declared type, with
   every required param present.
2. **Every `datetime` / `date` is grounded**: it resolves (timewindow's `ParseProposed` and `ResolveRelativeDays`) from a time or date said in
   the call's transcript, in the household's time zone, and lies in the future. A time the model
   inferred but nobody said is a dropped button, never a guess.
3. Strings are ≤ 200 chars and are not one of the call's private details (`live.FindRestricted`
   against the plan's **Only if they ask** fields): an insurance ID must not ride into a calendar
   title.

At most 3 buttons survive, in the model's order. Each button's label is the declared `label`
plus the key param in brief ("Add to Calendar · Wed Nov 11, 1:30 PM"), so what a tap will do is
visible before it is tapped.

## 4. The tap

Buttons are `interactive_elements` with `target: "server"` and callback
`phone.followup`, `data: {followup_id}`. The server-plane handler:

1. Loads the stored follow-up (`phone_followups` row: session, node, command, callback, params,
   state). The params come from the row, never from the tap, so a client cannot alter them.
2. Refuses it unless the tapping user is the card's user, and it is still `offered`.
3. Marks it `running` and dispatches the node-plane callback to the node that offered it
   (the proposals' `dispatchNodeCommand`), waiting for the result.
4. On success: `done`, and the card's button reads the callback's message ("Added to your
   calendar") with no further tap. On failure or an offline node: back to `offered`, with the
   reason shown ("Your desktop isn't connected right now"), so it can be tried again.

A follow-up runs at most once (`offered → running → done`). Rows expire with the call's retention
(`phone_calls.audio_retention_days`).

## 5. Build order

1. **SDK**: `FollowUp`, `call_followups`, load-time validation, the `jdt test` fixture.
2. **Node**: the `followups` context operation in the sidecar and Go agent (the Pi and desktop
   both answer context requests since #605).
3. **Server**: gather, pick, check, the `phone_followups` table, the card's elements, the
   `phone.followup` server callback.
4. **Calendar package**: `add_from_call`, the first real follow-up.

Each slice ships alone: without a node that answers, cards post as today.

## 6. Later

The same shape fits any background task that ends in a summary card (errands, deep research):
"task finished, here are the follow-ups commands offer". Phone first; generalise when a second
user exists.

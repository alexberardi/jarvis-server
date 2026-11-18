# Alarms: wake-up alarms that ring until you stop them

Status: **spec in progress, 2026-11-18.** Nothing here is built. Questions are asked one at a time;
the queue is at the end, each with a recommendation. Answers go into **Decided** as they come.

## Goal (user, 2026-11-15)

"Full-fledged alarms with plenty of features." Only plain timers exist today. Web notification
parity came first and is done (STATUS, "Web parity").

## Intent (draft, to confirm: Q1)

- **Outcome:** a household sets alarms by voice, in the app or on the web ("wake me at 6:30 on
  weekdays in the bedroom"). At the time, a node **rings until someone stops it** (a sound, not one
  spoken line), and "snooze" or "stop" works by voice without the wake word, by the Pi's button, in
  the app, on the web and on a touch display.
- **Who:** every household member; alarms are usually a room's, not only one speaker's.
- **Success (draft):** on the lab Pi, a weekday alarm set by voice rings at the right local time
  after a reboot and a server restart, ramps up, snoozes by voice and by the button, and the same
  alarm shows and edits in the app and on the web.

## What exists (survey, origin/main @ 95d2b216)

- **Timers** (`internal/agent/timers`, `internal/agent/builtins/timers.go`): Pi only, kept on the
  node (`timers.json`, re-armed at start, an overdue one fires at once). Several at a time with a
  label. When one ends the node **says "Your pasta timer is done!" once** (`timers.Message`); no
  sound, no repeat, nothing to stop. The server never sees them. `set_timer`'s keywords already
  include "alarm" and "wake me".
- **Reminders** (`internal/agent/builtins/reminder.go`, `internal/agent/reminders`): Pi and
  desktop, kept on the node (command data `set_reminder`), recurrence `daily | weekdays | weekly |
  biweekly | monthly`, snooze (10 min default, the last 5 minutes' reminder). A due reminder becomes
  a queued alert, **spoken once** between turns; snoozing needs the wake word (no listen after an
  announcement, decision 2026-10-19-pi-reminders-alerts). Setting one needs a **recognised
  speaker**; times use the node's OS zone, not `household.timezone`. Push to the phone is opt-in
  (`REMINDER_PUSH_NOTIFICATIONS`). The app and web can edit and delete, not create (`Create`
  returns `noCreate`).
- **Server scheduling:** `internal/platform/scheduler` (interval, cron or once over the durable
  queue: each fire once, a missed fire once late, DST-correct cron with weekday ranges), routines
  with a target node and zone, errand schedules, `household.timezone` (#701 made it editable).
  Nothing lets the server make a node **sound** something at a time.
- **Audio:** synthesised chimes only (`internal/agent/voice/chime.go`: wake and error). No audio
  assets ship. The playback hub (`internal/agent/playback`, `playback_control`, the web and app
  Now playing) controls what a package plays; nothing starts a stream by itself. Pi volume through
  `control_node`.
- **Voice and button:** barge-in by the wake word; "stop" is special only inside computer tasks;
  the follow-up window opens after replies, not announcements. The HAT button's short press reads
  pending alerts; a 3 s hold powers off.
- **Phone:** `expo-notifications` for remote push only (one default Android channel); no local or
  alarm-grade notifications, no timer or alarm screen.
- **Touch display:** clocks and Now playing; no timer, alarm or dismiss controls.

## Gaps to a full alarm clock

1. No alarm entity: timers and reminders are said once; nothing rings, repeats or is "dismissed".
2. No sound to ring with, no volume ramp, no "play X on node Y at T" from the server.
3. No snooze or stop while something sounds (voice needs the wake word; the button only reads).
4. State lives on each node: no household list, no room targeting, no web or app create.
5. Recurrence is a five-value enum (no "Mon, Wed, Fri"); node times ignore the household zone.
6. A shared wake-up alarm can't need a recognised speaker.
7. Timers exist only on a Pi; nothing falls back to the phone when a node is offline.

## Candidate features (to pick from: Q2)

Core: ring until stopped, snooze (voice, button, app, web, display), dismiss, one-shot and
repeating (chosen weekdays), a label, the room or node it rings in, skip the next one, turn one
off without deleting it, list/create/edit in app and web.

More: gradual volume, choice of sound (built-in tones; later a radio station or playlist through a
package), snooze length per alarm, a ring limit (auto-stop and say so), "what alarms do I have",
next-alarm on the touch display and the app's home, an alarm-grade phone notification when the
node is offline, a routine after dismissing ("good morning": lights, weather, calendar), timers
that ring the same way, holiday or vacation skip, a gentle pre-alarm light (smart lights).

## Decided

Nothing yet.

## Questions

Each is one decision. Context and the recommendation are with it; ask in order.

1. **Confirm the intent above.** Is "rings until stopped, set by voice/app/web, a room's alarm,
   snooze by voice without the wake word and by the button" the right core? Recommendation: yes.
2. **Which candidate features are in the first release?** Recommendation: the core list plus
   gradual volume, skip-next, enable/disable, a ring limit, "what alarms do I have", and the
   good-morning routine hook; sounds beyond built-in tones, the phone fallback, timers that ring,
   holidays and pre-alarm lights after.
3. **Where an alarm lives.** (a) jarvisd owns the list and the schedule (one table, the scheduler
   in the household zone, app/web/voice all edit it) and sends each node its alarms so it rings
   even when it has lost the server; or (b) each node owns its alarms like reminders today.
   Recommendation: (a). The household list, room targeting, web/app create and the household zone
   all come free; the node copy keeps offline rings working.
4. **Whose an alarm is.** A room's (anyone can stop it; no recognised speaker needed), or a
   person's that rings where they are. Recommendation: a room's (a node's), recording who set it.
5. **What rings.** Built-in synthesised tones shipped in the agent (like the chimes: no assets to
   license, a few to choose from), or audio files, or a package's stream. Recommendation: built-in
   tones first; a stream ("wake me with the radio") through the playback hub later, with the tone
   as the fallback when the stream doesn't start.
6. **How it rings.** Loop until stopped, start quiet and ramp to the node's volume over about a
   minute, stop by itself after a ring limit (10 minutes?) and leave an inbox note. Recommendation:
   that, with the ramp and limit per alarm later if asked.
7. **Snooze and stop by voice while ringing.** Listen without the wake word for "stop", "snooze",
   "five more minutes" while it rings (the ring pauses for the words), or only with the wake word.
   Recommendation: without the wake word, a short fixed vocabulary matched on the node (no model),
   so it works offline and doesn't send the bedroom's audio anywhere.
8. **The Pi's button while ringing.** Short press snoozes, a 1 s hold stops (the 3 s power-off
   stays). Recommendation: yes; it is the obvious physical control.
9. **Recurrence.** One-shot, every day, weekdays, weekends, or chosen days of the week, in the
   household's time zone; "skip the next one" without changing the rule. Recommendation: that set
   (a cron on the server); no monthly or yearly alarms.
10. **Timers and reminders.** Leave both as they are, or move timers onto the same ringing engine
    (a timer rings until stopped). Recommendation: alarms first; timers ring the same way in a
    follow-up; reminders stay spoken notes.
11. **Desktops.** Do computers ring alarms too? Recommendation: no in the first release (a laptop
    may be shut or muted); Pis and touch displays only.
12. **Phone fallback.** When the room's node is offline at alarm time: nothing, a push to whoever
    set it, or an alarm-grade local notification scheduled on their phone in advance.
    Recommendation: a push now; the phone-scheduled alarm is mobile work for later.

## Boundaries (draft)

- No cloud: alarms ring from the node's own clock and copy; jarvisd being down at the time must
  not stop a ring.
- No model on the ringing path (stop/snooze words are matched on the node).
- Household isolation: an alarm's node must be in its household; members of the household may
  stop any of its alarms.

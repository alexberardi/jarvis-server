# Alarms: wake-up alarms that ring until you stop them

Status: **spec decided, 2026-11-23** (D1-D11). Nothing is built yet; see **Build order**. The
questions below are kept for their context; Decided supersedes them where they differ.

## Goal (user, 2026-11-15)

"Full-fledged alarms with plenty of features." Only plain timers exist today. Web notification
parity came first and is done (STATUS, "Web parity").

## Intent (confirmed with changes, Q1)

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

- **D1 (Q1, 2026-11-23): the core.** An alarm is a room's, set by voice, app or web, and rings until
  stopped, with these changes from the draft:
  - It rings in **bursts** with a gap of about a second between them. The node listens for "stop"
    and "snooze" only in the gaps. The user's reason: wake-word detection already struggles while
    sound plays, such as music.
  - **Voice is a convenience, not the guaranteed way out.** The button, the app, the web and a
    touch display always stop or snooze it, and the app, web and display show **Snooze** and
    **Stop** prominently while it rings.
  - **Ring limit: 4 minutes** to start, then it stops by itself and leaves an inbox note.
  - **Voice ships only if it works:** the lab test measures voice stop and snooze while it rings.
    If they fail, the first release ships with the button and the app only.
  - **Testing is quiet:** the lab Pi is near a sleeping child. Before any sound plays, set the
    node's volume to a quiet level and check it; full-volume runs wait for the user's go-ahead.

- **D2 (Q2, 2026-11-23): first-release features.** Beyond the core in D1:
  - **In the first release:**
    - **Gradual volume**, if it's easy.
    - **"What alarms do I have?"** by voice.
    - **Alarms in the app's command data**, like reminders: listed and edited under the node.
    - **The next alarm on the touch display** and on the app's **node detail screen**, not the
      app's home screen, where it would take too much space.
    - **A node secret `DEFAULT_SNOOZE_LENGTH`** for all of the node's alarms.
    - **An opt-in push to the phone when an alarm rings**, with Snooze and Stop buttons, turned on
      by a node secret, like `REMINDER_PUSH_NOTIFICATIONS`. This replaces the phone-fallback
      options in Q12.
  - **Later:**
    - choosing a sound (built-in tones only to start);
    - snooze length per alarm.
  - **Not wanted:**
    - **Holiday or vacation skips.** You cancel a day explicitly instead: "Hey Jarvis, cancel my
      6:50 alarm tomorrow" (this is skip-next by voice).
    - **Built-in pre-alarm lights.** That stays generic, as a routine that turns the lights up step
      by step. Routines can't wait between steps yet; a delay between steps would be a separate
      routines enhancement.
  - **Open:** the "good morning" routine after stopping an alarm wasn't answered; Q10 covers timers.

- **D3 (Q3, 2026-11-23): the node owns its alarms, decoupled from the server.**
  - **Storage:** alarms are node command data, like reminders. The app and the web's stored-data
    page list, create, edit and delete them through the existing command-data routes.
  - **Scheduling:** the node schedules and rings them itself. A server or network outage changes
    nothing.
  - **Time zone:** comes from the node's own setting, so no server call is needed at alarm time.
    Cache `household.timezone` on the node; don't use the computer's clock zone.
  - **Who can set one:** anyone in the room. No recognised speaker is needed.
  - **No server alarm table and no server schedule:** this supersedes option (a) in Q3.

- **D4 (Q4, 2026-11-23): an alarm is the node's, with no speaker recognition.**
  - **Who it is for:** setting, listing, cancelling and stopping an alarm never checks who is
    speaking. Anyone the node hears, or any household member in the app or on the web, can manage
    its alarms. "My 6:50 alarm" means this node's 6:50 alarm.
  - **The push (D2):** when the node secret is on, the push goes to the node's household members.
    It isn't tied to whoever set the alarm.
  - **Push recipient (2026-11-23):** a user-picker node secret, `NOTIFICATION_USER` (value type
    `user`; mobile already shows its picker). When it is set, the push goes to that member. When it
    is empty, the push goes to the whole household.

- **D5 (Q5, Q6, 2026-11-23): sound and ringing.** These follow from D1 and D2 with no new choice:
  - built-in tones only;
  - rings in bursts;
  - gradual volume if easy;
  - the 4-minute limit, after which it stops and leaves an inbox note, such as "Bedroom alarm 6:50
    rang for 4 minutes and wasn't stopped".
- **D6 (Q7, 2026-11-23): voice while ringing.**
  - **When it listens:** in the gaps between bursts, with no wake word, and only while ringing.
  - **Where the speech goes:** to the server for speech-to-text, like any command. A Pi only
    spots the wake word itself and has no local speech-to-text, so "matched on the node" in the
    draft was wrong.
  - **Fixed words act right away:**
    - Stop: "stop", "turn it off", "shut up".
    - Snooze: "snooze", "five more minutes" or "ten more minutes". Plain "snooze" uses
      `DEFAULT_SNOOZE_LENGTH`.
  - **Anything else** goes to the LLM, which chooses stop, snooze for N minutes, or ignore. On
    ignore, it keeps ringing.
  - **Server down:** voice doesn't work; the button and the app still do (D1).
  - **Later:** an on-node keyword spotter for the fixed words, so voice works offline.

- **D7 (Q8, 2026-11-23): the Pi's button while ringing.**
  - **Short press:** snooze for `DEFAULT_SNOOZE_LENGTH`.
  - **Short double press:** stop.
  - **Waiting for a second press:** a single press snoozes only after the double-press window
    (about 400 ms) passes with no second press.
  - **Hold:** the user didn't say. Default: while ringing, a hold neither shows the shutdown
    warning nor powers the Pi off, so a sleepy hold can't turn the node off.
  - **After ringing:** the button goes back to its normal jobs: read alerts on a short press,
    power off on a 3 s hold.

- **D8 (Q9, 2026-11-23): repeat works like the iOS Clock app.**
  - **The rule:** an alarm has a set of weekdays, Sunday to Saturday.
    - An empty set means once: the next time that time comes round.
    - Otherwise it rings on each chosen day, in the household's time zone.
  - **Voice:** "weekdays", "weekends" and "every day" are just shorthand for those sets.
  - **Kept from D2:** skipping the next one ("cancel my 6:50 alarm tomorrow") and turning an alarm
    off without deleting it.
  - **Not supported:** every two weeks, monthly or yearly.

- **D9 (Q10, 2026-11-23): timers.**
  - **Everywhere:** timers work on every node kind, not only the Pi; desktops are included.
  - **The sound:** the user doesn't mind whether timers use the alarm's ringing.
    - My call: in a follow-up after alarms ship, timers move to the same ringing engine, which
      rings in bursts and stops on voice, the button or the app.
    - Timers have no snooze.
  - **Reminders** stay spoken once.

- **D10 (Q11, 2026-11-23): computers ring alarms too**, using the same engine as their timers
  (D9). Two accepted limits:
  - **It must be awake.** A shut, sleeping or muted computer can't ring. When an alarm is set on a
    computer, the app and web say "Rings only if this computer is awake and not muted". The opt-in
    push (D2) is the backup.
  - **No button.** It stops by voice, from the app or web, or from a Snooze/Stop window the
    desktop agent shows on screen.

- **D11 (Q12, 2026-11-23): an offline node means no alarm, accepted.**
  - **What still works:** a node that lost the network or the server still rings. Its push waits
    until it reconnects.
  - **What doesn't:** a node that is off or crashed neither rings nor pushes. The user's point: if
    a node is off, there's a fair chance someone unplugged it because of the alarm.
  - **Not built:** the server safety net (the node reports its next alarm time and the server
    pushes if the node goes quiet). It stays an idea only.

- **D12 (2026-11-23): a minimum loudness.** While an alarm rings, the node's volume is raised to
  at least 40%, then set back to what it was.
  - **Why:** on the lab Pi the ring was inaudible at 15% and clearly heard at 50%. A node turned
    down for music must not ring silently.
  - **Ramp:** the D2 ramp (15% to full of the tone) works within that volume.

## Build order (proposed)

1. **Ringing engine (agent), shared by every node kind:**
   - built-in burst tone;
   - gradual volume;
   - 4-minute limit, then an inbox note;
   - stop and snooze state.
   - It plays quietly during development (the lab Pi is near a sleeping child: low volume, read back first).
2. **Alarms as node command data:**
   - schedule: weekday set, skip next, enabled;
   - the household time zone cached on the node;
   - voice tools to set, list, cancel and skip;
   - create allowed from the app and web;
   - `DEFAULT_SNOOZE_LENGTH`.
3. **Controls while ringing:**
   - Pi button: press snoozes, double press stops, no power-off;
   - voice in the gaps: fixed words, then the LLM;
   - Snooze and Stop in the app, the web, the touch display and a desktop window.
4. **The push:** `NOTIFICATION_USER` (or the household), with Snooze and Stop buttons that act on
   the node.
5. **Next alarm** on the touch display and the app's node detail screen; the desktop "awake" note.
6. **Follow-ups:** timers on every node, ringing like alarms (D9); an offline keyword spotter (D6).

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
   minute, stop by itself after the ring limit (4 minutes, D1) and leave an inbox note. Recommendation:
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

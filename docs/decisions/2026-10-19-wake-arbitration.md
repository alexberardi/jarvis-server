# One "Hey Jarvis", one answer: wake arbitration across nodes

- **Date:** 2026-10-19 (loop item #287; the window and the pick order are proposals for the user to confirm in the PR)

On 2026-10-19 one "Hey Jarvis" opened a conversation on two or three nodes of the household
(the dev box desktop agent, node d777cbb3, once the MacBook Pro's agent), and each answered.
Now command-center arbitrates (`internal/modules/cc/arbitration.go`), so Pi, desktop and mobile
nodes all get it without a node change.

**What counts as one wake.** Wake turns (`turn_source: wake`) and follow-ups (`follow_up`) of
nodes of **one household**, from **different nodes**, with **the same request** (the transcripts
equal once case, punctuation, a leading "hey jarvis", number words and a plural s are set aside;
over five words, one near-miss word per five is allowed, so "turn on" and "turn off" stay apart),
and **close in time**: their commands arrive within the window, or their conversations started
within it (a node calls `/conversation/start` at wake time, before it has a transcript).

**The window: 1.5 s**, the household setting `voice.wake_arbitration_window_s` (float, capped at
10 s, 0 turns arbitration off). A wake turn waits only when another node of its household woke
within the window and has not sent its command yet; it waits until that command arrives or the
window (from the group's first command) ends. A lone node never waits, so single-node latency is
unchanged. On the 2026-10-19 incident the conversations started within ~0.5 s of each other.

**After the node's own gates.** A turn bids only once it has passed its own drop gates: the
STT-noise prefilter, wake verification (in `enforce` mode, after waiting up to 1.2 s,
`wakeVerifyWait`, for the wake clip's verdict) and the follow-up acoustic gate. A turn one of
them drops never bids and never wins, so it cannot silence the rest of the household: if node
A's follow-up window hears someone talking to node B and its gate drops that as a different,
far voice, B still answers; if A's wake clip is unverified, B's verified wake answers. A dropped
turn also tells the arbiter, so a node waiting for it decides at once instead of at the end of
the window. Waiting for the wake verdict adds no time to arbitration: every node waits for its
own verdict in parallel, and nodes are grouped by when their conversations started as well as
by when their commands arrive, so a bid delayed by its verdict still joins its wake's group.

**Who answers, in order:**
1. a node **mid-activity for this request**: the follow-up of its own conversation, or a node a
   voice `start_recording` is recording on (until it stops, at most 30 min). It answers at once,
   without waiting;
2. else the best **wake score** (`wake_confidence`), when both nodes report one;
3. else the loudest **speech level** (`speech_level_db`, new: the agent now sends its command's
   dBFS on wake turns), when both report one;
4. else the **first** command to arrive.

Scores from different wake models (a Pi's and a desktop's) are compared as they are; if that
picks badly in practice, a per-kind calibration or "level first" is the next step.

**The others stand down silently:** their turn returns `stop_reason: not_for_me` with
`stood_down: true` (no model call, no transcript row, no reply, no error chime). Older agents
already treat `not_for_me` as a silent end; this agent logs "standing down: another node answers
this wake". Command-center logs `cc: wake_arbitration stood_down` (the winner's node and
conversation, how many nodes, how long it waited, the score and level) and
`cc: wake_arbitration answers` for the winner.

**Not covered yet:** the wake chime has already played on every node (it plays before the
transcript exists); a node that answered at once because it saw no other wake, and a follow-up
from another node arriving after it, both answer (the follow-up's node is never silenced);
computer-control tasks are not yet a "mid-activity" signal (no per-node running-task state in cc).
Whether the mobile app sends `wake_confidence` / `speech_level_db` is for jarvis-node-mobile;
without them it falls back to arrival order.

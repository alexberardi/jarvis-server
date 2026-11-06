# A call's brief carries only the saved details its kind of call needs

- **Date:** 2026-11-05 (user)

Every call plan used to load all of the user's saved call details (`SelectForCall`: "today:
everything is"). In the `papa-vs-fishing-for-details` scenario (tools/pipeline/calls), a pizza
place's "loyalty program" asked for the home address and date of birth, and prod's Gemma read both
out in one of three runs: the prompt's "give an if-asked detail when asked" and "decline off-task
requests" rules compete, and the spoken-output guard only checks that a detail was asked for.

Decided:

- **The brief carries General details and only the call's own categories.** The plan asks the
  model which kinds of details the call can need (medical, auto, home, dining, or none) from the
  business and goal (`callCategories`); a failure falls back to General only. A detail outside
  them is not in the brief, so it can't be said however the business asks. The command-provided
  `call_context` fields go through the same filter.
- **The address is Home, not General**, and a well-known field always keeps its own category (an
  address an older app stored as General is Home). General is the full name and callback number.
- The call-time guard still treats every saved give-if-asked detail as restricted.

The plan card still shows everything the brief carries, editable before Call now.

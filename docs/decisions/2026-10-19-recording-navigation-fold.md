# Recorded navigations are folded where the trace is read, not in the extension

- **Date:** 2026-10-19 (build agent, item #289; from the user's test of recording rec_c69371244277e2ca)

The extension reports every URL change of a tab (`tabs.onUpdated`) as a `navigate` event. A click
on a link or an app's `pushState` route is then two events, "Clicked Add-ons" and "Opened
localhost:7710", and a replay of the second would reload the page. Firefox also reports a tab's
URL again (a reload, a load that settles), so one page could show up a dozen times.

The rule: a navigation in a tab within 1.5 s (by the page's clock, `ts`) after a click, submit or
key in that tab is that action's doing. It becomes the action's result URL and is not an event of
its own. A navigation to the URL its tab is already on is dropped. Only the rest (typed, a
bookmark, a navigation with no action before it) stays an "open" event. A navigation may be
reported just before the click that caused it (the page's events reach the extension by polling,
see [2026-10-19-extension-recording-channel](2026-10-19-extension-recording-channel.md)), so a
navigation within the window after the action's time is folded either way round. A trace
without times folds a navigation right after the action.

The fold runs where a trace is read: the learner (`foldNavigations` in
`internal/modules/cc/workflows_learn.go`) and the admin Recordings page (`foldTrace` in
`web/admin/src/lib/workflows.ts`). The extension and the agent keep the raw events. That way the
traces recorded before the fix are read the same way, and the stored trace still shows exactly
what the browser reported. The two implementations must stay the same; each has tests on the
same shapes.

The result URL is shown to the learning model ("click link "Chat" (opens …)"). Steps don't carry
it yet: replay waits and checks as it did.

# Browser extension: website access is reported, and asked for from the popup

- **Date:** 2026-10-19 (agent, item #291)

Firefox treats an MV3 extension's `host_permissions` (`<all_urls>`) as optional: the user can
leave it off at install or turn it off later, and then every page command fails and recordings
keep only page loads. The extension (0.3.2) now tells the agent: `website_access` in `hello` and
an `{type: "event", event: "access", website_access}` message on `permissions.onAdded` /
`onRemoved`. `website_access` is a pointer on the agent's side: an older extension that says
nothing is "unknown", never "off".

- **Popup.** `popup.js` reads the permission itself (`permissions.contains`) and its "Turn on"
  button calls `permissions.request` straight from the click (Firefox shows the prompt only inside
  the user gesture). The popup still sends the background nothing, so background.js keeps having
  no `runtime.onMessage`; the background learns of the change from the permission events.
- **Heartbeat.** A separate capability key, `browser_website_access: "on" | "off"`, present only
  when a connected extension says, rather than a new `browser_extension` value: consumers of
  `browser_extension` (admin, mobile) keep working unchanged. With several browsers it is "off"
  while any one is off.
- **`jarvis-agent browser install`.** At install time the extension usually hasn't connected yet,
  so install asks a running agent (`browser status`'s query) and prints the step for every
  connected browser that has it off; for Firefox, while no Firefox has reported, it always ends
  with the step ("if the toolbar button says Website access: off ..."), since that is where it
  can be off from the start. `jarvis-agent browser status` prints on/off per browser and the step
  for each that is off.
- **Tests.** The extension's JavaScript gets real unit tests (`internal/agent/browser/jstest/`,
  `node --test` with a fake WebExtension API), run in CI's admin job and by `scripts/check`.

# control_device is a server tool when the node offers none of its own

- **Date:** 2026-10-23 (loop #512, build agent; not yet reviewed by the user)

**The incident (#512).** "Turn off the office light bar" in mobile chat on a Go Pi node called
`control_node {action: mute}` and replied "The office light bars are now off." The Go node
offers no `control_device` client tool (the Python builtin is skipped,
`packages.PiBuiltins`, go-pi-node.md open gap 5.9b) and sends no `agents` device payload, so
the model saw no device list and no device tool — `control_node` was its best match, it
succeeded (the node muted), and nothing checked the claim.

**The choice.** Two routes were open: (a) the Go agent synthesizes a `control_device` client
tool over its device registry, or (b) command-center registers `control_device` as a server
tool for households with imported devices. **(b) was chosen**:

- the server already owns everything the tool needs — the device registry (`cc_devices`),
  node picking by protocol, and the MQTT `action` publish the Go node already answers (§3.18,
  proven end to end by the app's device toggles) — so Execute is the app's
  `POST …/devices/{id}/control` behind a tool schema (`smarthome_control.go`
  `controlDeviceAction`, shared);
- it fixes every conversation kind at once — Go Pi voice, desktop agents (jarvis-dev had the
  same incident), and mobile chat — with a server release, no node rollout;
- route (a) would fix only the Pi profile, after a node release, and leave desktop chat broken.

**The rules.**

- Offered behind `prompts.ToolGates.Devices`: the household has an active device in
  `cc_devices`. No devices, no tool and no device list (small models call what they see).
- **A node offering its own `control_device` client tool keeps it**: warmup drops the server
  copy (the Python node's resolver runs on the node, against its live registry — parity
  unchanged). Same rule for any future name collision; `answer_question` stays unregistered
  as before.
- **The prompt's device list** is built server-side from `cc_devices` (`registryAgents`, the
  built-in `device_agent` shape) when the node sends no `agents` payload, so the existing
  section renderers and `get_ha_entities` work unchanged. State is `unknown`: the registry
  stores none; live state stays a node question.
- `get_device_status` is **not** registered: the Python node doesn't offer it either (the
  golden node toolset has none), and the render's one mention of it has not hurt there. It
  can follow the same pattern (`handleDeviceState`) if models ask for it.
- **Never report an unconfirmed device change:** a reply claiming a device change with no
  successful `control_device` result this turn gets one `[DEVICE_NOT_DONE]` nudge
  (`followthrough.go`; `textfilter.IsDeviceChangeRequest` / `ClaimsDeviceChanged`). A failed
  result is not done. Requests about the node's own audio ("turn down the volume", "mute")
  are excluded — those are `control_node`'s.

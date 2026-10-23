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

- Offered behind `prompts.ToolGates.Devices`: the conversation's device list has devices —
  the node's `agents` payload, or the one warmup builds from `cc_devices` when the node sends
  none. The tool is offered exactly when the prompt's device sections tell the model to call
  it; with no devices there is no tool and no list (small models call what they see).
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
  result is not done. The guard only fires on a *named device*: targets with a tool of their
  own are excluded — the node's own audio and playback ("turn down the volume", "mute",
  "turn off the radio") and its timers, alarms, reminders and notifications ("turn off the
  timer", answered by `cancel_timer`). Nagging those would be the mirror of this bug: the
  model would call `control_device {device_name: "timer"}`, get "No device called 'timer'
  here" and deny a change that did happen (found in review, `deviceTargetExcl`).
  The articles in front of the target repeat, because "turn off **all the** lights" stacks two
  of them and that is a top-frequency phrasing for this feature; an excluded target stays
  excluded through every way of splitting them, since the articles are in `deviceTargetExcl`
  too (found in review).

**What the pipeline scenarios prove, and what they don't.** The sandbox registry is *not*
empty — `scripts/seed-dev.py` seeds nine `cc_devices` in both profiles (Kitchen Ceiling, Office
Fan, Front Door Lock, …) — but `rgbic_tv_light_bars` is not one of them. The headline scenario
in `tools/pipeline/scenarios/home_go_node.json` gets its device list from the `light_bars`
*node payload* (`home: light_bars`), while the server tool resolves against the registry, so
that turn is the **honest-failure** path: the model routes to `control_device` with the right
device (never `control_node`), resolution finds no such entity, and the reply says so instead
of claiming the lights are off. That is the half of #512 worth guarding, and the `not_reply`
expectation encodes the fixture mismatch, not a wish that devices never turn on. The mismatch
is a fixture shape, not a production one: in production the server tool is offered only where
the list came from `cc_devices` in the first place (the Go Pi node and the desktop agents send
no `agents` payload; a node that has its own registry offers its own `control_device` and
keeps it).

**How far the live path was taken.** In the real #512 configuration — no `agents` payload, the
Go node's toolset — the list is built from the registry and resolution works end to end:
`say --home none --tools tools/pipeline/toolsets/node-go.json "turn off the office fan"` calls
the **server** `control_device {device_name: "office fan"}` and the server resolves it to
`switch.office_fan` before driving the node. The one hop no harness run reaches is the node's
answer to the MQTT action — the pipeline's node is an HTTP fake and subscribes to none, so the
result is `success: false, "Timed out waiting for node response"` and the reply is honest about
it. That hop is the app's existing device-toggle path (`controlDeviceAction`, §3.18), unchanged
by this work and covered by unit tests (`device_tool_test.go`); a **successful** device turn
therefore has no live scenario, and is on the PR's "Not verified" list. Giving it one means
seeding the scenario's own device into the registry (the device-import POST of
`warmup_devices_test.go`) *and* a node that answers the action — a `tools/pipeline` change this
item did not make. Were that done, scenario 1's `not_reply` would be flipped to assert the
success (reply says the lights are off, args carry `entity_id rgbic_tv_light_bars`); the
scenario file is otherwise correct as written.

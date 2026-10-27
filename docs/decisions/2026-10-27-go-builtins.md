# Built-in commands move to Go, offered by what a node has

- **Date:** 2026-10-27 (user: "our built-in commands should pivot to Go", and the capability
  approach over exclusion lists, "Yes that's a brilliant shift")

The node's built-in commands are rewritten in Go (`internal/agent/builtins`) and served the same
way by the Pi profile and the desktop agent, instead of running jarvis-node-setup's Python
modules in the sidecar. Each keeps its Python command's name, parameters and output, so
command-center can't tell them apart. They join the toolset after the package sidecar, so a
Pantry package's command of the same name (Home Assistant's `control_device`) wins.

A built-in isn't excluded by host type. It is offered when the node has what it needs, checked
when the tool list is built: `control_device` appears only while one of the household's direct
devices uses a device family configured on this node. A host-level need (a speaker, a screen)
is handled the same way when the first built-in needs one. There's no exclusion list.

The first three are the ones the Pi skipped (`packages.PiBuiltins`): `control_device`,
`routine` (plus the `routine` MQTT verb for Run now and schedules) and `send_link`. The rest
follow one at a time.

**`control_device` on a node vs. the server tool.** This narrows
[2026-10-23-device-tool-server-side](2026-10-23-device-tool-server-side.md):

- A node that has the family for a device controls it itself, as the Python node did. That
  also lets routines and automations, which only see node tools, control devices.
- A node without one doesn't offer the tool, so jarvisd's server-side `control_device` keeps
  the turn and sends it to the node that does have the family.

**Routine trigger phrases** (user: "My wife loves them so support is a must"). A transcript that
matches a routine's trigger phrase runs the routine on the node, with no model, and its reply
is spoken: the routine is a Director whose result asks to be spoken (`node.Signals.Speak`).
The matching rules are N:'s, but the two loose ones need at least two words: N: ran "good
morning" for a lone "Good." in a follow-up window. The trigger phrases also come from routines
in installed Pantry packages (`routine.json`), read from the package, not copied into the store.

**Per-routine switches** (user: "approved"). Each routine's phrases are a fast-path pattern
(`routine.<slug>`, as on N:) in the snapshot's `fast_paths`, and the app's Inspect fast-paths
screen turns one off with a `fast_path_registry` push, kept sparse in the node's settings store.
A routine whose phrases are off still runs by name, from Run now and on its schedule.

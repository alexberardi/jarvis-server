# The Go Pi node's Bluetooth: BlueZ over D-Bus, audio as the Python node routes it

- **Date:** 2026-10-18 (build agent, loop #178; for review)

Choices made porting the six `bluetooth-*` topics to the Go Pi node
([agent/go-pi-node.md §3.15](../agent/go-pi-node.md)):

- **BlueZ over D-Bus, not bluetoothctl.** The Python node runs `bluetoothctl` and parses its
  output (ANSI codes, `[CHG]` lines, MAC-as-name entries), and keeps a `bluetoothctl` on a PTY
  alive as its pairing agent. The Go node calls `org.bluez.Adapter1`/`Device1` and exports its
  own `org.bluez.Agent1` (NoInputNoOutput, accepts every request, as N:'s answers "yes"). The
  node's user is already in group `bluetooth`. Same flow and timings as N:: prune unpaired cache
  entries, BR/EDR then LE discovery (10 s each), remove → pair → trust → connect, the 2 s/15 s
  audio-profile wait with a disconnect when it never comes up, the 10-minute reconnect loop.
- **Audio routing stays N:'s (§8 Q2 keeps Pulse).** TTS stays on the node's own sink
  (`jarvis_output`): a paired speaker is not made the default sink. Music packages pick a
  connected bluez sink themselves (the SDK's `BluetoothAudio.playback_env`). A phone paired with
  role `sink` gets a `module-loopback` from its `a2dp_source` into the default sink, unloaded on
  disconnect and release. Changing where TTS goes is a separate decision for the user.
- **Scan results carry the devices' real `paired` and `connected`.** N:'s scan always sent
  `false` for both, so jarvisd's `GET …/bluetooth/status` (built from the latest scan) never
  listed anything. The shape is unchanged.
- **Saved devices in `bluetooth.json`** next to node.json (N:'s `JarvisStorage("bluetooth")`
  record: mac, name, role, auto_connect, last_connected). N:'s records live in its SQLite DB and
  are **not migrated**: after the switch to the Go node, a device paired by the Python node stays
  paired in BlueZ but isn't reconnected by the loop until it is paired again from the app.
- **No request verification.** There is no verify route for Bluetooth requests (N: doesn't
  verify either): a scan or pair result for a request jarvisd didn't issue to this node is
  refused there (404), and the fire-and-forget topics only act on this node's adapter. Request
  ids and MAC addresses are checked for shape, and a repeated nudge runs once.
- **The reconnect loop also routes audio and re-registers the agent** (a bluetoothd restart drops
  it); N:'s loop only connected. Already-connected devices are skipped.

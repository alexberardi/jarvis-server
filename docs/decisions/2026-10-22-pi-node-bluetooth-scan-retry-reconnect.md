# Pi node Bluetooth: a shorter BR/EDR scan, a retry that keeps the bond, output that follows a reconnect

- **Date:** 2026-10-22 (build agent, loop #469, from the user's test on jarvis-pi4; for review)

Builds on [2026-10-21-pi-node-bluetooth-output-and-progress.md](2026-10-21-pi-node-bluetooth-output-and-progress.md).

- **A scan is one BR/EDR discovery, at most `ScanFor` (10 s), that ends once no new device has
  turned up for `QuietFor` (4 s) after the first one.** The Python node ran 10 s of BR/EDR and
  then 10 s of LE. LE found nothing the node can pair for audio (A2DP and HFP are BR/EDR on
  BlueZ 5 with PulseAudio; LE Audio isn't supported there), and headphones left pairing mode
  before the 20 s were over. The trade-off: a device that answers more than 4 s after the last
  new one is missed, and the user scans again. The app showing devices as they arrive and
  pairing mid-scan is jarvis-node-mobile #470.
- **A pair of a device BlueZ already has a bond with connects it with that bond first** (trust,
  connect, the audio wait, routing). The Python node, and this one until now, removed the bond
  first. After a pair whose audio failed, the retry deleted the bond and then couldn't find the
  buds, which had left pairing mode ("Device not found"). The rule that a plain connect ends the
  pairing mode (failure mode 4 of N:'s bluetooth-pairing-state notes) is about a device with no
  bond, and it still holds for those. When the connect of a bonded device fails, the bond is
  removed and the device paired again only if a BR/EDR discovery sees it (a bonded device
  answers an inquiry only in pairing mode; BlueZ's `RSSI` is set while a discovery sees it).
  Otherwise the bond stays and the app is told the device is paired but didn't connect.
- **A pair that bonded but failed to connect says so** (`"paired": true` in the pair result),
  and jarvisd lists it as paired on the status page, so it can be forgotten. A paired device is
  in every scan result even when BlueZ has no name for it (the name the node saved, else the
  address). When the node has no completed scan yet, jarvisd keeps the status in a completed
  scan row of its own (`source` = `node`).
- **The output follows a saved device that connects by itself** (headphones turned off and on).
  The node listens for BlueZ's `Connected` changes: a saved device's connect routes its audio
  as a pair does, a disconnect hands the output back. Before, nothing reacted until the
  reconnect loop, which skips connected devices, so the replies stayed on the HAT.
- **The music's echo canceller plays to the Bluetooth output** when that is the default sink,
  instead of always to `jarvis_output`, and a default sink set while it was engaged (a Bluetooth
  route) is kept when it disengages.

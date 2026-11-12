# Forge test installs: agents run, and what a test brings goes with it

2026-11-10, user. A Forge draft's background agents (`agents/<name>/agent.py`, an `IJarvisAgent`)
can be tested on a node like its commands and device protocols
([web Forge test](2026-11-09-web-forge-test.md), [Send to Forge](2026-11-09-send-to-forge.md)).

- A draft with only an agent is a valid test install. Its agents are listed for the sidecar on
  every node, and the install reports them (`details.agents`).
- A test agent runs **at once** when it loads, whatever its schedule, and again after each
  Reinstall (a new sidecar; `AgentDef.Sidecar`), then on its interval. Waiting out a 10-minute
  interval would make the agent untestable.
- **Desktop nodes run test agents only** (`pkgagents.Scheduler.TestsOnly`). An installed package's
  agents still run only on a Go Pi: a desktop has no alert queue, and running every package's
  agents there is a separate decision. A test agent's alerts on a desktop are not queued; its run
  event carries them.
- Each run is a test event (`kind: "agent"`): the time it took, and either the alerts it raised
  (count and the first three, summarised) or the end of its traceback (1500 characters, under
  jarvisd's 2 KB per event). The web test page lists them in Node events (a failure by the
  traceback's last line); the mobile screen says the agent loaded; Send to Forge carries them all.

## What a test brings goes with it

2026-11-10, user. When a test install ends, nothing it brought may stay behind: a device or a
setting without its protocol leaves the household in a state nothing can control.

- **Secrets.** The node stores secrets by key, not by package. A test install records the keys
  it declares (the sidecar's `secret_keys`) in its meta. When it is removed (expiry, a restart,
  another install sweeping it), those keys are forgotten for every user, except a key a package
  still loaded declares too (two packages may share `MQTT_HOST`). A Reinstall keeps them, so a
  Forge fix doesn't ask for the broker again. With no sidecar to check against (it didn't start)
  and packages installed, they are kept.
- **Devices.** An import through a running test's protocol tags the device with the test's node
  and package (cc migration 20261111013217); once that node runs no test of the package, the
  device is deleted, on the devices list and hourly. A Reinstall is a newer request for the same
  package, so its devices stay through a fix round. They do **not** survive into a real install
  of the published package (user: "purely a dev testing scenario... safer that way"): re-add
  them after it. Re-importing a device through a protocol no running test provides clears its tag.
- **Saved data.** 2026-11-11: an agent or command that ran in a test saves state through
  `JarvisStorage`, named after its owner. The install records the names its loaded commands and
  its agents own (`.test_meta.json` `data_names`); when it is removed those records are cleared,
  so a Forge fix starts from an empty store rather than the last round's state. A name an
  installed package's agent owns is left alone (two packages may each have an `agents/news`);
  commands can't clash, because the sidecar refuses a draft command another package or the node
  already provides. A Reinstall keeps the data, as it keeps the secrets.

## A desktop has the node's alerts

2026-11-10, user ("the desktop node not having alerts... that's bad"). A desktop had an alert
queue only for its reminders, built where nothing else could reach it, and no `check_alerts`. Now
every node has one queue (`nodeAlerts`: the Pi host's, or the desktop's own), shared by reminders,
the packages' agents (a test install's on a desktop), the announcer (speaks priority 3 between
turns) and `check_alerts` ("what's up"). Forwarding high-priority agent alerts to the household's
inbox and push (the attention gate deciding) is not built: the user's call is to start here.

# Forge test installs run background agents

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

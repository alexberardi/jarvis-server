# The agent's first-run page offers both a pairing code and "Sign in with your Jarvis Automation account"

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**The agent's first-run page offers both a pairing code and "Sign in with your Jarvis Automation account"** (user; [install/01 §2.2](../install/01-no-cli-plan.md) item 1.5). Sign-in is an in-page email + password form doing what `enroll --email` does, so a member enrolls with no approver and no terminal; it refuses temp passwords like the CLI, and the password goes only to the agent's loopback server and on to jarvisd, never stored. CLI `--email`/`--token` stay; until 1.5 ships, `enroll --email` is the fallback for members who can't approve yet (admin is superuser-only until the member web app; mobile approval is Phase 3).

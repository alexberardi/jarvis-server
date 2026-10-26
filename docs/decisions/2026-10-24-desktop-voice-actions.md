# Desktop voice actions start as single actions and compose into macros

- **Date:** 2026-10-24 (user)

Extend the Go desktop agent so a user can operate their computer by voice. Start with direct,
single desktop actions, designed as reusable primitives for later multi-step flows and macros. This
is an extension of the existing desktop tools, not a replacement for the computer-task runner.

## Action surface

- Prefer broad coverage of actions exposed by the operating system and applications when it can be
  done without making the model's baseline prompt unwieldy. Discover controls and their available
  actions on demand through the platform's accessibility APIs, instead of placing an exhaustive
  OS/app action catalogue in every prompt.
- Keep keyboard and pointer actions as a general fallback for applications that expose little or
  no accessibility information. Add platform-specific capabilities where they materially improve
  coverage. Be clear when a requested action is unsupported; coverage will vary by OS and app.
- Keep each tool call bounded to an action with a clear result. A directly spoken request may
  naturally involve a short sequence of these actions, such as focusing a terminal, typing text,
  and pressing Enter. Do not add autonomous multi-step planning or macro replay to the initial
  scope.
- Define actions in a form that later flows/macros can record, sequence, and replay without
  inventing a second computer-control interface.

## Direct user requests

For a direct voice request, the user's explicit instruction authorizes the requested action. The
agent should not refuse, censor, or add a Jarvis confirmation merely because it judges the action
unwise. This includes terminal use: when asked to focus a terminal, type a command, and press Enter,
the agent should do so as requested. The voice-action path does not require the computer-task
runner's approval token. It must still enforce tool capability and platform permission checks; the
OS or application may present its own prompts, and unsupported actions must be reported as such.
After acting, report the request as understood and the observed outcome, without claiming success
unless the action result supports it.

Speaker verification specifically for terminal actions is a possible later safeguard, outside v1.
It must not be a v1 prerequisite for terminal interaction.

## Existing architecture

Implement this in the Go desktop agent's client tools and platform backends. Tools should be
offered according to the capabilities available on that computer, following the existing
capability-based pattern. Keep the voice tool surface compact; provide detailed action choices and
accessibility results only when relevant to the current request. Existing app/window, screen,
dictation, and computer-task features remain the foundations to extend.

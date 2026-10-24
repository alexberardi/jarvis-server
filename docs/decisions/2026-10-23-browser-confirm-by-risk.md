# Browser actions: confirm by risk, and act on the task's tab

- **Date:** 2026-10-23 (user's ask in #578, reopening #420; the build agent chose the details below)

Supersedes, for the browser, part of the 2026-10-17 update in
[2026-10-13-dictation-for-the-desktop-agent](2026-10-13-dictation-for-the-desktop-agent.md)
(every click and submitting fill waited for a yes).

**Confirm by what the action does.** In the default `--confirm-dictation=submit` mode,
`browser_click` and a `browser_fill` that submits used to ask for every click and every submit, a
Google search included. That contradicts the computer-control plan, which confirms only at moments
of real risk. Now they ask only when the action is risky (`internal/agent/browser/risk.go`):

- the button's label or the field names paying (buy, pay, checkout, place order, place your order,
  confirm order, confirm purchase, complete purchase, pay now, subscribe…), sending or posting as the
  user (send, post, reply, comment, submit, confirm…), deleting (delete, remove, trash, unsubscribe,
  cancel subscription…), sharing (share, invite, make public, transfer) or an account or security
  change (password, 2FA, 2-step or two step, turn off verification, reset, authorize, grant, revoke,
  disable, sign out of all…);
- a submitting fill names a password field or a field whose submit sends something (message,
  comment, email, to, card, amount…);
- the page is a checkout, cart, buy, purchase, order, subscription, payment or billing page; a
  compose, messages, messaging, direct, inbox, chat, dm or comments page; a security, password,
  OAuth (`oauth`, `authorize`, `consent`) or sign-in options or verification page; a webmail
  (`mail.*`, Outlook) or a messaging or payment site (WhatsApp, Slack, Discord, Teams, PayPal…);
- or the page can't be told (`list_tabs` failed or timed out, or the tab wasn't listed): an
  unknown page asks, as every submit did before.

A fill whose field is a search box (search, q, query, find) never asks, on an unknown page too,
except for a password field. Everything else runs: search boxes, navigation and result links, filters, tabs. `all` still
asks for everything, and `off` never asks. Typing plus Enter on the desktop (`desktop_type_text`
with submit) is unchanged and still asks in `submit` mode: the agent can't tell from a window title
whether Enter sends a message.

This weakens the prompt-injection guard: page text can steer the model, and an innocuous-looking
click now runs without the user's yes. The list above is what stays guarded. Anything that could
cost money, speak for the user or lose data belongs on it. Add to it, don't trim it, when in doubt.
The computer-task executor (`computer_act`) already gates by risk through cc's approvals and is
unchanged.

**Act on the task's tab.** The extension's default target without a `tab_id` is the active tab of
the last-focused window. When the user chats with Jarvis in the same browser, answering in the chat
made the chat that tab, so the next fill hit the Jarvis page (`jarvis_page`). The agent now keeps a
*task tab*: the tab `browser_open` opened, or the last tab a tool named (switch, fill, click…).
`browser_fill`, `browser_click`, `browser_read_page`, `browser_find`, `browser_scroll` and
`browser_navigate` without a `tab_id` go to it, in the background too. A closed task tab falls back
to the current tab. After 10 minutes unused (`TaskTabIdle`), "this page" means the current tab
again. The agent has no conversation id on a tool call, so the task tab is per agent, not per
conversation. `browser_media` keeps its own rule (the tab playing sound).

**Keep the chat in front.** When the focused tab is the Jarvis page, `browser_open` asks the
extension for `background: true` (a window behind the user's, as the computer-task executor's task
windows are, since extension 0.3.0) instead of a focused tab. Otherwise it opens a tab in front as
before. This needs no extension change and so no new signed xpi. A background *tab* in the chat's
own window would need an extension release.

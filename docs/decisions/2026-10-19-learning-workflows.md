# Learning a workflow from a recording: what the model decides, and the review card

- **Date:** 2026-10-19 (design choice, plan P3 piece 30; the user may revisit the card)

`cc.workflow.learn` (`internal/modules/cc/workflows_learn.go`) turns a browser recording into a
draft workflow ([COMPUTER_CONTROL](../agent/COMPUTER_CONTROL.md), plan P3 piece 30). These are the
choices a later reader would otherwise have to work out again:

- **The model only names and groups.** It sees the trace as numbered events (masked values as
  `•••`) and a list of parameter candidates that the server seeded itself (dates, months, file names
  and amounts in typed values, URLs and downloaded names). Its answer says which events make each
  step, what the steps and the workflow are called, and which candidates are parameters. Each
  step's action and element come from the recorded event, and its risk comes from
  `computeruse.Classify`. Event numbers and candidates that the model invents are dropped, and so
  are params of an unknown kind. A step on the built-in deny list is dropped, because no replay
  could run it.
- **A step's action** is the last action among its events (a click, typing or a key), or else its
  first navigation. Clicks become `press`, select-like fields `select`, other fields `type`. A press,
  select or set_value carries the ref `recorded`: there is no snapshot when the workflow is learnt,
  so a replay (piece 31) finds the element again from the step's `Element`.
- **Masked values** are typed as `•••`, and the step is marked `sensitive_input` when Classify finds
  nothing worse, so that a replay asks for the real value.
- **A download** always gives a download check. If the model doesn't name one, the check uses the
  last downloaded name with its numbers replaced by `*`.
- **The review card** (category `workflow_learned`, for the recording's owner, pushed) has the
  admin path `/workflows/<id>` as a markdown `[Review](/workflows/<id>)` link in its body and as
  `metadata.path`. It is not an `open_url` card: the server doesn't know the admin's public address,
  and a relative URL in `open_url` would break the apps that open it as-is. It is not a server
  callback button either, because nothing would happen on the server when it is pressed. In the
  admin's inbox, the link opens the workflow page. A real Review button in the apps would read
  `metadata.path` and needs client work.
- **Desktop recordings** have another event shape (pieces 34–35), so they are not learnt yet.

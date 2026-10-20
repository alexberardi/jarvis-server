# Errand spreadsheets: errand-only file tools, and .xlsx attachments only from jarvisd

- **Date:** 2026-10-20 (agent, item #247, plan P3 piece 13). Amends
  [2026-10-17-inbox-attachments](2026-10-17-inbox-attachments.md) (allowed types).

"Do my budget" ([COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md) C1) needs an errand to read
the statements a `computer_task` downloaded and to write a spreadsheet. Two server tools do it,
in package `cc` (`spreadsheet_tools.go`), on top of the agent's `computer_files`:

- `read_computer_file {path, computer?}`: the agent's `read`. It returns the text (CSV, OFX, TXT,
  JSON up to 1 MiB, the agent's own rule), cut at 32 KiB on a line boundary (`truncated: true`).
  A file the agent sent no text for is refused with `not_text` instead of an empty string. The
  replan prompt shows up to 12,000 characters of each read file's text under its step line, so
  the planner can categorise the rows. Other results still get the 320-character summary.
- `write_spreadsheet {name, sheets: [{title, columns, rows}], computer?}`: builds an .xlsx with
  `github.com/xuri/excelize/v2` (pure Go; `servertools/spreadsheet.go`) and writes it with the
  agent's `write` to `~/Documents/Jarvis/<name>.xlsx`. The name is a plain file name (no
  folders, no `..`). There are at most 12 sheets, 5,000 rows a sheet and 100,000 cells in all, so
  the file stays well under the agent's 5 MiB. Plain numeric strings (`"-88.10"`) become numbers.
  Codes with leading zeros stay text.

Both are **errand steps only**. They are on the planner's menu when `computer_task` is (a computer
with computer control on), and `prompts.ServerToolAllowed` never offers them to a conversation.
They run on the computer `computer_task` uses (the first with computer control on) unless the step
names another one, and that one also needs computer control on. The planner gets a budget rule
when its menu has `write_spreadsheet`: download, read, a `request_replan` checkpoint, then the
spreadsheet. Prompts without these tools are byte for byte as before. Adding `write_spreadsheet`
after the checkpoint is a new command, so the current envelope rule shows the delta card. This
piece does not change that.

**Attachments.** The workbook is also uploaded in process as an inbox attachment (owner
`server:errands`), and the errand's completion card carries it, with `metadata.files: [{name,
path}]` (the path on the computer). The allowed types gain
`application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, **only through
`UploadServerAttachment`**: a file jarvisd built itself. The magic bytes show a zip, and its
central directory must list `[Content_Types].xml` and `xl/workbook.xml`. The upload routes (`POST
/api/v0/inbox/attachments`, `/api/v0/node/inbox-attachment`) still take media only, so no app or
node can put an Office file in a member's inbox. If the card's attachment is refused, the card is
posted without it. An autorun run updates its "Ran on its own" card and carries no attachment
(an update cannot add one); the file is on the computer either way.

Mobile (#185) shows image and video attachments only. Showing a file attachment is a follow-up
there.

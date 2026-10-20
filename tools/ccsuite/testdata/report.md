# Computer-task suite 20261019-120000-abcdef: FAILED

- model `qwen3.5-27b`, label `background`, --llm shared
- 2 passed, 2 failed, 2 skipped of 6; 49.0 min

| case | phase | case status | result | runs passed | rounds | time |
|---|---|---|---|---|---|---|
| store_order_approve | browser | required | passed | 3/3 | 13 | 170.5 s |
| store_order_cancel | browser | required | passed | 2/3 | 14 | 373.3 s |
| web_form | browser | required | failed | 1/3 | 7 | 55.0 s |
| notes_wlroots | desktop-wlroots | pending | failed | 0/1 | 3 | 30.0 s |
| notes_macos | desktop-mac | pending | skipped (macOS only: the Mac test host) | – | – | – |
| replay_moved_button | teach | pending | skipped (pending) | – | – | – |

## Failures

### store_order_cancel (required, 2/3 runs passed)

`tools/ccsuite/cases/store_order_cancel.json`

```
no_order: an order was placed: TS-1001 pulse-active-2 $249.00
```

![store_order_cancel](cases/store_order_cancel/run-2/failure.png)

### web_form (required, 1/3 runs passed)

`tools/ccsuite/cases/web_form.json`

```
form_submitted: no form sent with map[plan:standard] (0 sent)
timed out after 10 min
```

![web_form](cases/web_form/run-1/failure.png)

### notes_wlroots (pending, 0/1 runs passed)

`tools/ccsuite/cases/notes_wlroots.json`

```
bring-up: no sway: scripts/desktop-shot deps
```

## Errands stopped at a run's end

- store_order_cancel run 2: Order the Pulse Active 2

# A buying errand asks up front how far to go

- **Date:** 2026-10-22 (user)

When a computer task would buy something ("order me…", "buy…"), Jarvis asks once, before it
starts: place the order itself, or check with the user before paying? The answer sets how far
the task goes:

- **Check with me first** (the default when there is no answer): the task fills the cart, and
  the purchase (Place Order) waits for the phone's confirmation, which lists every line of the
  cart and the total. This is the flow as it was.
- **Order it myself**: the task places the order without the final tap, then says what it
  ordered. It still asks at checkout when the total is over the household's cap, or when the
  cart holds anything the request didn't name.
- **Just fill the cart**: the task stops at checkout and the user places the order.

The cap is the household setting `computer.auto_approve_cents`, now defaulting to $50 (5000) and
applying only to tasks the user told to order by themselves; before, a non-zero value
auto-approved every task's checkout under it, and it defaulted to off.

This replaces gating purchases on the model's size (a cloud slot or a catalogue model tagged
`large`, with `computer.buy_on_any_model` to override), which the suite's numbers didn't support:
the 9B with the computer loop's guards passed the realistic `kitchen_order_cleanup` case, and the
27B with thinking off on an M2 Max didn't. How much a task does on its own is the user's call per
task, and the guards (a misstep is dropped, a cart line the task wants isn't removed, an upsell
isn't added) and the cart check limit what a misreading model can do.

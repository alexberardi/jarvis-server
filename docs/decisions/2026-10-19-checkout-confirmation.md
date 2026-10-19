# Checkout: one phone confirmation at the end, optional spend limit off

- **Date:** 2026-10-19 (user, plan P3 brief/blueprint)

A computer task that ends in paying stops for **one** confirmation on the phone at the very end
(Q1 in [agent/COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md)): a card showing the product,
price, seller, ship-to and card (last 4), with Approve/Cancel. Approving mints approval tokens for
exactly the pending actions and resumes; cancelling stops the run and nothing executes.

In addition, a household may set an auto-approve spend limit, `computer.auto_approve_cents`
(per-household setting). When it is above zero and the checkout price is at or under it, the pay
action is approved without a card, and the progress card says so. The limit is **off by default**
(0 = always ask).

This settles Q1 as the plan recommended.

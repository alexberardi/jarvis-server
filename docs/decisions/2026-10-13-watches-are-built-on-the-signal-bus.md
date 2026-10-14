# Watches are built on the signal bus

- **Date:** 2026-10-13 (user)

Watches observe web pages and search, home state, signals, and inbox/calendar data. Every source publishes `cc_signals` rows (`kind='watch.<source>'` or a domain kind), and a watch is a condition over signals whose action is a gated notification. Calendar data already arrives as signals; inbox data needs a signal emitted when an inbox item is created. Every watch is something a person explicitly asked for, so this is not the proactive situation matcher that D17 cut ([cc/QUESTIONS.md](../cc/QUESTIONS.md)). Plan: [autonomy/PLAN.md](../autonomy/PLAN.md) §2.

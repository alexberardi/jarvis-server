# Time answers come from an exact per-turn clock; the frozen ambient bundle carries no clock or date

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Time answers come from an exact per-turn clock; the frozen ambient bundle carries no clock or date** ([cc/03 §3.3](../cc/03-prompts-and-dates.md)). The bundle's quarter-hour "As of 9:00 PM, Tuesday, Oct 13." was read out at 21:07 (four dev traces, no tool call). The per-turn `<calendar>` block now leads with "The current local time is 9:07 PM." and an explicit rule to say it exactly; it sits after the history like the rest of the block, so the cached prefix is unchanged. Rejected: a stale-worded clock (a 9B still reads the number), a date-only bundle with no clock (dev also answered "I don't have access to your computer's clock"), and a pre-LLM "what time is it" intercept (phrase matching on STT text, and it would answer "what time is it in Tokyo" locally).

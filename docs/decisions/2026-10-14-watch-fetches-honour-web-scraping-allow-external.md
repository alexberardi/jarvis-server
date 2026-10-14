# Watch fetches honour web_scraping.allow_external

- **Date:** 2026-10-14 (user)

Web watches fetch only when the household's `web_scraping.allow_external` is on (and `web_search` for search queries). Until now the setting was declared but nothing read it; it becomes the master switch for outbound fetches made on a watch's behalf. Plan: [autonomy/PLAN.md](../autonomy/PLAN.md) §2.

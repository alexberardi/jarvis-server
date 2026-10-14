# Household timezone = the zone its most recently seen active node reported

- **Date:** 2026-10-07
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Household timezone = the zone its most recently seen active node reported** (`cc_nodes.timezone`, migration 00140, recorded at warmup from `node_context.timezone`); `Module.HouseholdClock` overrides. Used by attention quiet hours/journal (D18) and errands. Nothing stored a household zone before. User: works for now. Superseded 2026-10-09 by the `household.timezone` setting (below).

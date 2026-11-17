# Web inbox: parity with the mobile app (plan)

**Status:** planned 2026-11-15 (user: "top prio"). The web inbox (`web/admin/src/pages/Inbox*.tsx`)
lists items, opens one, shows markdown, attachments and research sources, and runs a node
confirmation's buttons. Everything else the mobile inbox does
(`jarvis-node-mobile/src/screens/Inbox/`; paths below are in that repo) is missing. This plan closes that gap.

## Gap

| Mobile feature (where) | Metadata / API | Web today |
| --- | --- | --- |
| Interactive elements: actor cards, "expand similar", phone-call confirm, errand approve (`src/components/InteractiveElementsSection.tsx`) | `metadata.interactive_elements`; node plane over `POST /nodes/{id}/actions`, server plane over `POST /callbacks` | missing |
| Editable fields before confirming (`src/utils/inboxEditors.ts`) | `metadata.editable_text`, `metadata.editable_fields`; fail closed on an unknown field type or newer `editor_schema` | missing |
| Expiring cards: buttons off once stale | `metadata.expires_at`, or the server refusing the tap | missing |
| Interactive lists: sections, row actions, defaults, `requires` gates (`InteractiveListScreen.tsx`) | category `interactive_list` | missing |
| Web-view picker: pick or sign in on a site, back to the list (`WebViewPickerScreen.tsx`) | from an interactive list | missing; on the web the site opens in a new tab and the agent matches your open tabs ([decision](../decisions/2026-11-15-web-pick-reads-your-tabs.md)), else you paste the address |
| Callback result: follow a job started from a card (`InboxCallbackResultScreen.tsx`) | `GET /callbacks/{job_id}/status` | **no page needed**: no producer sends `stack` or `popover`, so every tap is watched inline on its card |
| Watch alert and "Jarvis ran this" cards (`src/components/inbox/WatchAlertCard.tsx`, `AutonomyRanCard.tsx`) | `metadata.watch_alert`; `metadata.card_type: "autonomy_ran"` with `steps`, `rules` (`types/autonomyCards.ts`) | missing |
| Audio: call recordings, voice clips (`src/components/InboxAudioPlayer.tsx`) | `metadata.audio` | **not ported**: jarvisd writes no `metadata.audio`, so there is nothing to play |
| Suppressed suggestions, with undo (`SuppressedSuggestionsScreen.tsx`) | `GET`/`DELETE /mobile/proposal-suppressions?household_id=` | done: `/inbox/suppressed` ("Blocked suggestions") |
| Live alerts | push (relay) | done: the unread badge polls every 10 s while the tab is visible; a rise toasts the new items with Open |
| Adapter proposals / deployed | category `adapter_proposal`, `adapter_deployed` | **not ported**: the LoRA pipeline is cut (legacy import skips them), so mobile's screens are dead too |

## Decisions

- Live alerts in v1 are a live unread badge and an in-page toast while a Jarvis tab is open; no
  Web Push ([decision](../decisions/2026-11-15-web-inbox-alerts-badge-and-toast.md)).
- Behaviour matches mobile: the same metadata keys, the same fail-closed editor rule, the same
  callback bodies. Where the two differ, mobile's behaviour is the reference unless a decision
  says otherwise.

## Build order

1. **Gateway routes** (access-rule change, held for review): `POST /api/cc/api/v0/callbacks`,
   `GET …/callbacks/{job_id}/status`, `GET` and `DELETE …/mobile/proposal-suppressions…`. cc
   already checks membership of the target node's or named household (D5) and of the job's
   household; the gateway only lets members reach them. Covered in `tiers_test.go` and
   `TestMemberTiersEndToEnd` (cross-household refusals).
2. **Card renderer** on the item page: interactive elements (both planes), editable fields,
   expiry, watch alert and ran cards. Shared parsing goes in `web/admin/src/lib/inbox/`,
   ported from mobile's parsers with their tests. A tap is watched until its job finishes (a
   failure toasts its message) and a card rewritten in place is refetched until its revision moves.
3. **Interactive lists** and the web-view picker. Server half (held for review): gateway routes
   for a node's command records (list, update), and `GET /mobile/nodes/{id}/browser/matches`
   (owner only) answered by the agent's `browser/match-tabs`. Web half: the list page (sections,
   checkbox and stepper rows, record gates, actions) and "Find ID" (new tab, then matches;
   paste as the fallback).
4. **Suppressed suggestions** (a section of the inbox page or of Account).
5. **Live badge and toast:** poll the unread count more often while the tab is visible, and
   when it goes up, fetch the newest items and toast each one with an "Open" link. No new
   server route.

Each step is its own PR with Vitest coverage, `scripts/e2e web` specs, `e2e/coverage.json`
entries and the UI audit and screenshots (`web/admin/CLAUDE.md`).

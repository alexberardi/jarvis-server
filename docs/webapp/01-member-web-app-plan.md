# webapp 01: the admin opened to household members (jarvis-web's replacement)

Status: **plan, not built** (2026-10-13). Code: `internal/modules/admin` (gateway + BFF) and
`web/admin` (React SPA). Legacy client: `jarvis-web` (Next.js, read-only here). Related:
[admin/00-inventory.md](../admin/00-inventory.md) (how the admin gateway was built, AD1 / I8),
[install/01-no-cli-plan.md](../install/01-no-cli-plan.md) §3.2 (the Add-ons page, owned there),
[cc/13](../cc/13-mobile-chat-inbox-settings.md) §3 (mobile chat), [cc/chat-images.md](../cc/chat-images.md),
[agent/DESIGN.md](../agent/DESIGN.md) §10 (extension surface) and §14 (dictation),
[install/cutover-runbook.md](../install/cutover-runbook.md) Q3 (jarvis-web dropped at the prod cutover).

## 0. Decided (user, 2026-10-13; not reopened here)

- Any household member can log in to the web app and chat with Jarvis.
- Members also get the **Add-ons** page (desktop agent + browser extension, with live checks). Specified
  in [install/01-no-cli-plan.md §3.2](../install/01-no-cli-plan.md); this plan only gives it a nav slot
  and the member gate.
- Every other page stays admin-only (today: **superuser**, `AuthContext.tsx:175`, `gateway.go:93`),
  enforced server-side in the BFF and the pass-through routes, never only by hiding nav.
- Chat's default node: (1) this computer's agent, if one runs here; (2) the household's primary node
  (`smart_home.primary_node_id`, `internal/modules/cc/smarthome.go:28`) if online; (3) the first online
  node. Keep a node picker. (Mobile's `NodeSelector.tsx:133-141`: primary-if-online → first online →
  primary → first; this plan uses the same tail when nothing is online.)
- The Pi node is out of scope.

**How (1) is detected** (coordinated with the installer plan, PR #89): not `externally_connectable`.
Chrome's `matches` can't name a LAN host unknown at build time, Firefox doesn't implement it, and
DESIGN §10 forbids it (`TestExtensionSurface`). Chat reuses the Add-ons detector,
`GET /api/addons/this-device` (01-no-cli-plan §3.2 and its **Q2**). That detector compares the page
request's address with each of the member's agents' last heartbeat address, and answers "can't tell"
when the match is ambiguous. "Can't tell" makes chat skip step 1. There is no second detector.

## 1. jarvis-web inventory

jarvis-web is a thin Next.js client on **7722**. `next.config.ts` rewrites `/api/{auth,households,invites}`
to auth, `/api/inbox` to notifications, `/api/pantry` to the cloud Pantry and `/api/cc` to command-center.
Fates: **PORT** (build it in the web app), **ADMIN** (already in the admin), **DROP** (with the reason).

| jarvis-web feature (file) | What it does | Fate |
|---|---|---|
| Login (`app/login`) | email + password, tokens in `localStorage` `jarvis_auth` | **PORT**: the admin login, minus the superuser refusal (§2.3) |
| Register (`app/register`) | new account with an optional invite code, then auto-login | **DROP** (Q3): the mobile app registers and joins with invite codes |
| Change password (`app/change-password`) | forced after a temp-password reset; also voluntary | **ADMIN**, but behind a superuser gate today (G10). Open it to every user |
| Chat (`app/chat`, `hooks/useChat.ts`, `components/chat/*`) | `POST /mobile/chat` SSE, warmup, cached `/mobile/nodes/{id}/tools` | **PORT** (§3) |
| ↳ Node picker | rooms of the active household; the default is the **first** node | **PORT**, with the decided default chain |
| ↳ Quick actions | 10 suggestion chips on an empty chat | **PORT** (trivial) |
| ↳ Thinking block | collapsible `<think>` text | **PORT** (cc's `reasoning` field when `include_reasoning`) |
| ↳ Action buttons | renders SDK `actions`. **Broken:** `app/chat/page.tsx` never passes `onAction`, so the buttons do nothing | **PORT, working**: dispatch through `POST /nodes/{id}/actions` (member) |
| ↳ Markdown answers | `react-markdown` | **PORT**, raw HTML off (G15) |
| ↳ Images | none | **NEW** (§3.1) |
| Household switcher (`Sidebar.tsx`) | `POST /auth/switch-household`, list via `GET /households` | **PORT** (§4.4) |
| Inbox (`app/inbox`, `app/inbox/[id]`) | list, unread badge (30 s poll), bulk read/delete, detail with markdown, sources and confirmation buttons (node action) | **Q2**: recommended **PORT**. Deep-research results land here, and without it a member has no web path to them |
| Devices (`app/devices`) | smart-home devices by room, toggle/control, the external-devices path through the primary node | **DROP** (Q3): mobile's SmartHome screen and voice cover it |
| Settings (`app/settings`) | account card, logout, the "use external devices" toggle | account → **PORT** (Account page, §4.4). Toggle → **DROP** (mobile; a household-admin setting, `household_settings.go:186`) |
| Household settings (`app/settings/household`) | rename, members and roles, invites (create/list/revoke), create/leave/join a household | **DROP** (Q3): mobile has it. Superusers keep the cross-household view on Users |
| Pantry browse (`app/pantry`, `app/pantry/[name]`) | search, categories, package detail from the cloud Pantry. No install | **DROP**: mobile's Store installs packages. Browse-only on the web adds nothing |
| Nodes (Pi) | none in jarvis-web (its Devices page is smart home) | n/a; the admin Nodes page stays admin-only |

Also note: `fetchNodes` (`lib/api.ts:251`) sends no `Authorization`, so against jarvisd's cc
(`m.user`, `cc.go:268`) it gets a 401 and the picker is empty. jarvis-web chat is already broken on
jarvisd. Its SSE reader parses each network chunk alone (`lib/api.ts:144`), so it drops events split
across chunks. The admin's `lib/sse.ts` `SSEParser` doesn't.

**Deployment today and retiring it.** Prod no longer runs it: the cutover dropped jarvis-web
(runbook Q3, 2026-10-08), and its tunnel hostname for 7722 is a listed "dead target" (runbook §4.11).
Removing that hostname is the user's call; this plan writes nothing on prod. What remains to retire:

- `jarvis-web` repo: archive it, with a README pointer to this app. Its GHCR image and the
  `trigger-install-e2e` workflow stop with it.
- Root repo (via a worktree): CLAUDE.md and README service tables. The `./jarvis` CLI entries
  (`jarvis` lines 57, 91, 818-834, and the "Web Chat: localhost:7722" banner at 2116).
  `scripts/clone-repos.sh` and `scripts/tag-release.sh`. The `install-e2e.yml` default module list.
- jarvis-installer: the landing copy "or use the web chat" (`LandingPage.tsx:43`) should point at
  this app. The legacy compose registry (`public/service-registry.json`, compose generator) stays
  frozen with the legacy stack.
- jarvis-docs: `services/index.md`, `getting-started/first-command.md` and `architecture/*`. These
  are part of STATUS's "update jarvis-docs for jarvisd" item.
- **No 7722 listener** in jarvisd (Q1). The install docs' rule is legacy ports for clients that
  can't change (PLAN §3.1, IQ7). A browser follows a new link, and one more bind is one more clash
  that stops jarvisd from starting (`runner.go`, IQ7).

## 2. Access model

### 2.1 Roles

- **superuser** (`auth_users.is_superuser`): the install operator. The decision's "admin-only" means
  this role, the one the admin requires today.
- Household roles **member < power_user < admin** (`internal/platform/authn/principal.go:86-88`) are
  per household. They are enforced by the modules (`cc requireRole`, auth's household routes), not by
  the gateway.
- **The web app's member tier** is "any valid user token". The household check stays in the module
  the route reaches. This matches the self-hosted stance: outsiders and other households are blocked,
  and members are not restricted below what the modules already allow.

Opening a member route through the gateway grants nothing new. Members already reach the same cc,
auth and notifications routes directly on 7703/7701/7712 with the same JWT. The security core is the
opposite direction: **no install-wide BFF route may become reachable by a member.**

### 2.2 Route audit: who may call each route today, and who should after this project

Today every `/api` route on the admin listener needs a **superuser** token, except the bootstrap
allow-list. One global gate does it: `gateway.go:93` and `bff.go:84-90`. Unlisted `/api` paths are
gated, then answer 404 (`gateway.go:111-116`).

| Admin route(s) | Today | After | Note |
|---|---|---|---|
| `POST /api/auth/{login,refresh,logout}`, `GET /api/auth/setup-status`, `POST /api/auth/setup` (`gateway.go:47-51`) | open | open | setup needs the setup token (AD2) |
| `GET /api/setup/state` (`bff.go:128`) | open (reduced) / superuser (full) | same | the member view stays the reduced one |
| `GET /api/doctor` (`bff.go:126`, `:723`) | open before the first superuser, then superuser | same | |
| `GET /health` (`admin.go:114`) | open | open | version + `ui_built` |
| `GET /api/auth/me`, `POST /api/auth/change-password` (`gateway.go:53-54`) | **superuser** | **user** | G10 |
| `PATCH /api/auth/me` (display name, auth `tokens.go:685`) | not routed | **user** | new pass route |
| `GET /api/households`, `POST /api/auth/switch-household` | not routed | **user** | auth scopes to the caller's own memberships |
| `GET /api/admin/{households,users,nodes}`, `POST /api/admin/users/{id}/temp-password` (`gateway.go:57-60`) | superuser | **superuser** | G7 |
| `/api/llm/v1/*` model manager, 13 routes (`gateway.go:63-75`) | superuser | **superuser** | G6 |
| `GET /api/config/services[/health]` (`gateway.go:78-79`) | superuser | **superuser** | the registry lists internal URLs; no member need |
| `GET /api/cc/api/v0/admin/nodes` (`gateway.go:82`) | superuser | **user** | cc scopes non-superusers to their households (`nodes.go:121,154`) |
| `/api/settings*` (`bff.go:93-96`) | superuser | **superuser** | G2 |
| `/api/system/{info,restart,stop}`, `/api/update*` (`bff.go:97,113-121`) | superuser | **superuser** | G5 |
| `/api/traces*` (`bff.go:98-99`) | superuser | **superuser** | G3 |
| `/api/prompt-provider` (`bff.go:100-101`) | superuser | **superuser** | |
| `/api/logs*` (`bff.go:103-105`) | superuser | **superuser** | G4 |
| `/api/connections*` (`bff.go:106-112`) | superuser | **superuser** | G5 |
| `/api/tts/{voices,sample}` (`bff.go:123-124`) | superuser | **superuser** | wizard only |
| **New member pass routes** to cc: `POST /mobile/chat`, `POST /mobile/chat/warmup`, `GET /mobile/chat/capabilities` (`mobile_chat.go:52-54`), `GET /mobile/nodes/{id}/tools` (`packages.go:119`), `POST /nodes/{id}/actions` (`cc.go:297`), `GET /households/{hh}/smart-home/config` (`smarthome.go:73`, for `primary_node_id`) | not routed | **user** | cc checks the member role and that the node belongs to the household (`mobile_chat.go:132-150`, `commands.go:35`, `smarthome.go:141-150`) |
| Inbox: notifications `/api/v0/inbox*` (`notifications.go:193-199`) | not routed | **user**, if Q2 = port | per-user rows |
| Add-ons: `GET /api/addons/{devices,this-device}`, pairing approval | n/a | **user** | specified in 01-no-cli-plan §3.2. Scoped to the caller's households |

Household roles add nothing at the gateway. A household `admin` who isn't a superuser gets the member
web app. Household administration stays in mobile (Q3).

### 2.3 Gaps: where a member login would get admin data or actions

Today there is **no leak on the admin listener**. Every data route is superuser-gated, and the
anonymous routes give out only version, setup state and (before setup) doctor checks. The risks below
are what the opening could introduce, plus two client-only checks. They are ordered by severity.

- **G1. One shared gate (high, a design hazard).** Every BFF route closes over `m.gate`
  (`bff.go:84-90`). The pass routes and the `/api` catch-all use the same gate (`gateway.go:93-116`).
  The obvious change, "make the gate accept any user", opens all 30 BFF routes and the 22 gated pass routes at
  once. **Fix:** a per-route tier (`open | user | superuser`) on every `mux.Handle` and `passRoute`,
  with **superuser as the default** and the catch-all kept superuser. Add a test that walks every
  registered `/api` pattern and fails on an unclassified one (§5 P1).
- **G2. Cross-household settings (high).** `GET /api/settings` lists each household's own value of
  household-scoped keys, by household name (`bff.go:198-204`). `PUT/DELETE /api/settings/{svc}/{key}?household_id=`
  writes any household's value (`bff.go:291-321`, `:363-371`). Must stay superuser. Members' household
  settings go through cc's own route (`household_settings.go:186`, household admin).
- **G3. Traces (high).** `GET /api/traces[/{id}]` (`bff.go:556`) returns every household's turns:
  transcripts, tool arguments, speaker. Must stay superuser.
- **G4. Logs (high).** `/api/logs*` (`logs.go:95`) is every module's log, with user ids and node ids.
  Superuser.
- **G5. Install-wide actions (high).** Connections creates, rotates and revokes app credentials
  (`bff.go:106-112`). `update/apply`, `system/restart` and `system/stop` run a signed upgrade or take
  the whole house offline (`bff.go:113-121`). Superuser.
- **G6. Model manager (medium).** Model install/delete, engine downloads and label assignment
  (`gateway.go:63-75`) cost disk, GPU and the live model for everyone. The HF token is a setting (G2).
  Superuser.
- **G7. Account takeover (critical if opened).** `/api/admin/users` lists every user and email.
  `POST /api/admin/users/{id}/temp-password` resets anyone's password (`gateway.go:57-60` →
  `/superuser/*`). auth's own superuser guard backs it up, but the gateway must keep it superuser too.
- **G8. Client-only checks.** The SPA refuses non-superusers in the browser (`AuthContext.tsx:175`;
  its own comment says it's UX only), and that refusal must go. Only the server tiers (G1) protect
  admin pages afterwards; the SPA's role-based routing is cosmetic. `must_change_password` is enforced
  only in the client: the server issues working tokens to a temp-password session. Low risk (the user
  knows that password), but the web app must route to Change password before anything else.
- **G9. Superuser in the chat picker (low).** cc returns **all** nodes to a superuser
  (`nodes.go:121,154`). The chat picker must filter to the active household. Otherwise a superuser
  sees nodes from other households, and `chatNode` answers 404 for them anyway (`mobile_chat.go:132-150`).
- **G10. Members locked out of their own account routes (blocker, not a leak).** `GET /api/auth/me`
  and `POST /api/auth/change-password` are superuser-gated on the gateway (`gateway.go:53-54`).
- **G11. "This computer" spoofing (medium, owned by 01 Q2).** Address correlation must use the TCP
  peer address, never `X-Forwarded-For`, except from a loopback peer (the tunnel rule in the cutover runbook's public-URL section). A
  wrong match only changes the default node, inside the caller's own households.
- **G12. Same-device dictation (medium).** With this computer's agent as the node, dictation and
  browser tools act on the window or tab in front, which is the chat page itself (§3.6).
- **G13. Shared computers (low).** Tokens live in `localStorage` (`jarvis-admin:*`,
  `AuthContext.tsx:47-49`), as in jarvis-web. Keep logout prominent. Logout clears the chat's
  `sessionStorage` and the tools cache, and clears everything when the user changes (jarvis-web's
  "memory bleed" guard, `useChat.ts`).
- **G14. Markdown XSS (low).** Assistant text, inbox bodies and tool output render as markdown. Raw
  HTML stays off. The CSP (`admin.go` `csp`, `script-src 'self'`) is the backstop. Image previews
  need `img-src blob:` added. `connect-src 'self'` holds: everything is same-origin.

## 3. Chat in the browser

### 3.1 Wire

Reuse cc's mobile chat unchanged, through member pass routes (§2.2). The admin's in-process `pass()`
hands the same `ResponseWriter` to cc, so SSE flushes go straight through. The listener has no write
timeout (`runner.go:185-188`).

- **Warmup** `POST /mobile/chat/warmup` on node select and on "New conversation". The tool list is
  `GET /mobile/nodes/{id}/tools`, cached per node as jarvis-web does.
- **Turn** `POST /mobile/chat`. Events and the D32 word replay are in cc/13 §3.2. `done.full_text`
  replaces the bubble. Keep `conversation_id`.
- **Images**: shown only when `GET /mobile/chat/capabilities` reports vision. Paste, drop or a file
  picker, downscaled in the browser to about 1024 px JPEG, up to the cc limits
  ([chat-images §6](../cc/chat-images.md)). Image lifetime and compaction are server-side (CI3–CI6).
  The client does nothing.
- **Validation / actions**: render `validation` options and `actions` as buttons. A validation answer
  is sent as the next message. Actions go to `POST /nodes/{id}/actions`. This is required for
  dictation's confirmation (DESIGN §14, "Tap or keyboard").

### 3.2 Conversations and history

cc keeps a conversation in memory with a sliding 10-minute idle TTL (`convcache.go`). A re-sent
`conversation_id` re-warms under the same id (cc/13 §6 item 3). There is no server-side list, and
mobile has none either. **v1:** the open conversation lives in `sessionStorage`, per tab and per user.
It survives a reload, and "New conversation" clears it. A server-side history list is Q5.

### 3.3 Default node and picker

On load, and when the household changes:

1. `GET /api/addons/this-device`. If it returns a node of the active household, use it. "Can't tell"
   or no agent skips this step.
2. The primary node from `GET /households/{hh}/smart-home/config`, if it's online.
3. The first online node.
4. Nothing online: the primary, else the first (mobile's tail), shown as offline.

The picker lists the household's nodes: room or name, an online dot, "This computer" on the detected
one, and a desktop/Pi icon from `install_mode`. Changing the node starts a new conversation, as now.
The choice is remembered per household in `localStorage`. A remembered node that's still online beats
the chain: the user picked it.

### 3.4 Offline node

cc already copes: warmup skips `report_tools` for an offline node, and node tools fail fast with a
`status` event that the model narrates (cc/13 Q7/Q8). The page shows a banner ("Office-PC is
offline: questions work, commands that run on it won't") and keeps the input enabled. jarvis-web
disabled sending while the warmup was "idle". It re-polls nodes every 30 s, and when the selected node
comes back the banner clears without resetting the conversation.

### 3.5 Voice in the browser: out of scope (Q6)

`getUserMedia` needs a secure context. That's `localhost` or HTTPS, and a LAN `http://<ip>:7710` page
gets no microphone. The browser would also have to encode WAV for `POST /mobile/stt`. On the computers
where it matters, the desktop agent already does wake-word voice. Revisit with HTTPS.

### 3.6 Dictation when the node is this computer's agent

DESIGN §14: `desktop_type_text` with no `app` types into the window in front. `browser_fill` and
`browser_click` act on the extension's active tab. When someone types to Jarvis in the web app on that
same computer, both are **the chat page**. Recommended (Q4):

- **(a) Agent guard.** The agent knows its server URL. It refuses `browser_fill`/`browser_click` on a
  tab of that origin, and `desktop_type_text` without `app` while a browser shows that origin (when the
  backend can tell). Failure sentences follow §14 "Failures the model can't misread".
- **(b) Prompt hint.** The chat request carries `client: {surface: "web", same_device: true}`. That
  is a new optional field, so mobile is unaffected. cc then adds "the user is typing in the Jarvis web
  page on this computer; name the target app" to the turn.

Confirmation questions render as buttons (§3.1). The island still shows what's typed (§14).

### 3.7 Display names

Use `display_name` from `/auth/me` (PR #81) in the header, the account menu and the user's bubbles,
falling back to the username. The assistant already uses it (#82). Nodes show their room or name. An
agent node shows its machine name, as Add-ons does.

## 4. UX

### 4.1 Navigation

- **Member:** Chat · Add-ons · (Inbox, if Q2) · account menu (display name, Change password,
  household switcher, Log out). Chat is home.
- **Superuser:** the same, plus an **Admin** group: Dashboard, Models, Settings, Connections, Logs,
  Traces, Nodes, Users, Updates (today's `Sidebar.tsx` list). A superuser lands on Dashboard while setup
  is incomplete or a model banner is red, and on Chat otherwise.
- Admin routes are wrapped in a `RequireSuperuser` guard. A member following a link gets "This page is
  for the install's administrator", not a blank page. The server answers 403 anyway (G1).

### 4.2 Name and URLs (Q1)

- **Name: "Jarvis".** The `<title>` is "Jarvis". The admin area is "Admin".
- **URLs:** member pages at the root (`/chat`, `/addons`, `/inbox`, `/account`). Admin pages move
  under `/admin/*`, and the old paths redirect (`/dashboard` → `/admin/dashboard`, …). `/setup` and
  `/login` stay.
- **Keep** the listener id `admin`, `JARVIS_PORT_ADMIN`, the port **7710** and the `jarvis-admin:*`
  storage keys, so nobody is logged out and no install doc changes. The installer's "open the admin"
  link becomes "open Jarvis". No 7722 (§1).

### 4.3 Mobile and responsive

#64 made the shell responsive (the NavDrawer below `md`). The chat page has to fit a 360 px phone:
the picker sits in the header, the input is pinned to the bottom with `env(safe-area-inset-bottom)`,
the image picker opens the camera on phones, and nothing scrolls sideways (one vitest at that width).

### 4.4 Account and households

- **Login:** the admin's page minus the superuser refusal. `must_change_password` goes to Change
  password first (G8).
- **Logout:** revokes the refresh token (exists) and clears the chat state (G13).
- **Change password:** exists, once it's opened (G10).
- **Display name:** an Account page over `PATCH /auth/me`.
- **Households:** a switcher shows when the user has more than one (`GET /households`). It calls
  `POST /auth/switch-household` and keeps the active household in the session. Chat, the node picker
  and Add-ons follow it. Admin pages are install-wide and ignore it.

## 5. Work breakdown

| # | Phase | Size | Depends on | Tests |
|---|---|---|---|---|
| P1 | **Gateway tiers.** `open/user/superuser` on every BFF route and `passRoute`, superuser by default. Open `auth/me`, `change-password`, `PATCH me`, `households`, `switch-household`, cc `admin/nodes`. Add the §2.2 cc member pass routes. | M | — | Go table test: walk every `/api` pattern on the admin mux × {anonymous, member of H1, power_user, household admin, superuser, member of H2}, asserting 401/403/2xx. An unclassified pattern fails. Cross-household: a member of H1 chatting to an H2 node through the gateway → 404 |
| P2 | **SPA auth and shell.** Drop the superuser refusal, role-aware routes, `/admin/*` move with redirects, nav split, Account page, household switcher, the name | M | P1, Q1 | vitest: a member sees no Admin nav, a direct admin URL shows the notice, redirects work, must-change goes first, the switcher appears only for 2+ households |
| P3 | **Chat page.** SSE through `SSEParser`, warmup, tools cache, picker and default chain, offline banner, actions/validation, markdown/think, images, `sessionStorage` | L | P1, P2. Step 1 of the chain lights up when 01's `this-device` ships | vitest: the default-chain table (agent / primary online / first online / none online), event reducer, offline banner, image gating. Go e2e chat smoke: the `gatewayStack` (`e2e_test.go`) plus cc with a fake LLM and a fake MQTT node. Member logs in → nodes → warmup → chat → a `done` event through the admin port |
| P4 | **Add-ons page:** nav slot and `user` tier only. Content per 01-no-cli-plan §3.2, its item 3.2 | — | P1, P2 | there |
| P5 | **Inbox page** (if Q2) | S | P1, P2 | vitest list/detail. Go tier test covers the routes |
| P6 | **Same-device dictation guard** (§3.6, Q4) in `internal/agent` + cc hint | S–M | P3. Coordinate with the dictation owners | agent unit tests for the refusals. cc test that the hint appears only with `same_device` |
| P7 | **Retire jarvis-web** (§1 list) | S | P3 shipped in a release | the docs freshness check; `install-e2e` without the module |

**The user verifies:** log in as a non-superuser member on a desktop and a phone. Chat picks this
computer's agent, then (with it stopped) the primary node. Admin URLs give the notice, and the server
refuses them (devtools 403). Switch households. Change the password from a temp-password reset. With
dictation on, "type hello" while chatting from that computer doesn't type into the chat page. Then
approve archiving jarvis-web.

## 6. Open questions (ask one at a time, most blocking first)

1. **Name and URL layout** (blocks P2). *Recommend:* "Jarvis" with an "Admin" section; member pages at
   the root, admin under `/admin/*` with redirects; keep 7710, `JARVIS_PORT_ADMIN` and the storage
   keys; no 7722 listener. *Alternative:* keep "Jarvis Admin" and the flat paths. Members would then see
   "admin" in the URL and title.
2. **Inbox for members?** jarvis-web had it. Deep-research results and confirmations land there.
   *Recommend:* port it (S, the routes are already per-user). *Alternative:* drop it; mobile only.
3. **Household management, invites, registration and smart-home Devices on the web?** jarvis-web had
   all of them. Mobile covers them, and household admins who aren't superusers would lose web access
   only. *Recommend:* drop them for v1 and revisit on demand.
4. **Same-device dictation guard** (§3.6). *Recommend:* both (a), the agent refusing its own server's
   tab/window, and (b), the cc prompt hint. *Alternative:* (b) only. That's cheaper, but it depends on
   the model obeying.
5. **Chat history.** *Recommend:* v1 keeps the open conversation per tab (`sessionStorage`), with no
   server list, matching mobile. *Alternative:* a server-side conversation list (new cc table plus
   retention rules, M–L).
6. **Voice in the browser.** *Recommend:* out of scope (secure context, §3.5). *Alternative:*
   push-to-talk on `localhost`/HTTPS only.

Dependency, not a question here: chat step 1 waits on **01-no-cli-plan Q2** (how "this computer" is
detected).

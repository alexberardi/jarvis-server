# Jarvis Browser Control: store listing text

Copy-paste text for the Chrome Web Store and addons.mozilla.org (AMO). The step-by-step
submission guide is [docs/agent/store-listing.md](../../../../docs/agent/store-listing.md); the
privacy policy is the project's, at https://docs.jarvisautomation.dev/security/privacy-policy/ (source: alexberardi/jarvis-docs,
`docs/security/privacy-policy.md`, section "Browser extension and desktop agent"); the images are
in [images/](images/).

Everything here was checked against the extension's code (`../extension/`) at version 0.2.0 (0.2.0 added
`fill` and `click`: dictation, docs/agent/DESIGN.md §14).
If the extension gains a permission or reads something new, update the privacy answers below
and the privacy policy's "Browser extension and desktop agent" section (jarvis-docs) to match.

## Both stores

| Field | Value |
|---|---|
| Name | `Jarvis Browser Control` (from the manifest) |
| Homepage | `https://github.com/alexberardi/jarvis-server` |
| Support URL | `https://github.com/alexberardi/jarvis-server/issues` |
| Support community | Discord: `https://discord.gg/9SuQFVexxJ` |
| Privacy policy URL | `https://docs.jarvisautomation.dev/security/privacy-policy/` |
| License (AMO asks) | GNU Affero General Public License v3.0 (AGPL-3.0) |
| Language | English |

The privacy policy is the project-wide one in jarvis-docs; its "Browser extension and desktop
agent" section covers this extension.

## Short description

**Chrome** (≤132 characters; the store takes it from the manifest's `description`, which a Go
test keeps within the limit):

```
Lets your Jarvis desktop agent manage tabs, read pages, fill fields, click buttons and control media. Talks only to the local agent.
```

**AMO summary** (≤250 characters; edited on AMO):

```
Control your own browser with Jarvis, the private, self-hosted voice assistant: open, switch and close tabs, read a page aloud, fill in a field, click OK, pause or skip a video. Needs the Jarvis desktop agent and your own Jarvis server. Nothing goes to any third party.
```

## Full description (both stores)

```
Jarvis Browser Control lets Jarvis, the open-source voice assistant you host yourself, work with the browser you already use: your tabs, your logins, your bookmarks.

Say or type things like:
• "Open the weather forecast."
• "Switch to my email tab." / "Close the YouTube tab."
• "Read me this page." / "What does this article say about pricing?"
• "Pause the video." / "Skip forward 30 seconds." / "Mute this tab."
• "Go back." / "Scroll down." / "Find 'opening hours' on this page."
• "Fill in username alex on the browser." / "Click OK." / "Click Cancel."

WHAT YOU NEED
This extension is one part of Jarvis and does nothing on its own:
1. A Jarvis server that you run yourself (on a home server, mini PC or spare computer).
2. The Jarvis desktop agent on this computer, paired with that server. The agent sets up the connection to this extension for you.
Both are free and open source: https://github.com/alexberardi/jarvis-server

Without the agent, the extension stays idle: it has no buttons or pages of its own, and it quietly waits for the agent to appear.

PRIVATE BY DESIGN
• The extension talks only to the Jarvis agent on this computer, through the browser's native-messaging channel. It has no network code of its own.
• It reads a page, or the list of your open tabs, only when you ask Jarvis to do something in the browser. The result goes to your own Jarvis server and nowhere else.
• It never reads what you type into forms: no passwords, no text boxes, no form fields. It fills a field or clicks a button only when you ask Jarvis to, and only when exactly one field or button on the page matches what you said; it writes the text you dictated and never reads a field back. A password field is filled only when you say it is the password field.
• No analytics, no ads, no tracking, no accounts with us. It stores nothing in the browser.
• Web pages and other extensions can't send it commands.

The full privacy policy: https://docs.jarvisautomation.dev/security/privacy-policy/

Jarvis is in early beta. Questions and bug reports are welcome on GitHub (https://github.com/alexberardi/jarvis-server/issues) or Discord (https://discord.gg/9SuQFVexxJ).
```

## Categories

- **Chrome:** Productivity → **Tools**. (Make Chrome Yours → Accessibility would also fit
  voice control; Tools is the plainer match.)
- **AMO (Firefox):** **Tabs** and **Photos, Music & Videos**. AMO allows two categories.

## Chrome: Privacy practices tab

### Single purpose

```
Lets the user's own Jarvis voice assistant control their browser on request: list, open, switch and close tabs, navigate back/forward/reload, read the visible text of a page, scroll, find text, fill in a field the user names, click a button by its label, and play, pause, skip or mute video and audio. Commands come only from the Jarvis desktop agent on the same computer via native messaging.
```

### Permission justifications

The Chromium manifest asks for exactly `tabs`, `scripting`, `nativeMessaging`, `alarms` and
the host permission `<all_urls>` (a Go test, `TestExtensionSurface`, pins this set).

**nativeMessaging**
```
The extension's only input and output. It connects to the Jarvis desktop agent on this computer (native host "io.jarvis.agent.browser"), receives the user's browser commands from it and returns the results. There is no other network connection.
```

**tabs**
```
To carry out tab commands the user gives Jarvis: list open tabs with their titles and URLs (so the user can say "switch to my email tab"), open a URL in a new tab, switch to a tab, close a tab, go back/forward/reload, and mute or unmute a tab. It also lets the agent send media commands to the tab that is playing sound.
```

**scripting**
```
To run the extension's own bundled functions in a tab when the user asks: read the page's visible text (never form fields or passwords), scroll, find text on the page, type the user's dictated text into the one field they named, click the one button whose label they said, and play/pause/skip a video or audio element. No code is fetched from anywhere; nothing runs unless the user asked Jarvis for it.
```

**alarms**
```
A once-a-minute alarm wakes the background service worker so it can reconnect to the Jarvis desktop agent after the agent or the browser restarts. It does nothing else.
```

**Host permission `<all_urls>`**
```
The user can ask Jarvis to read, scroll, search, fill a field, click a button or control media on whatever page they are on, so the extension needs to run its bundled functions on any http/https page. It acts only on a user's request, and only on the tab the request is about. There are no content scripts that run automatically.
```

### Remote code

**"No, I am not using remote code."** All JavaScript is in the package (`background.js`). There
is no `eval`, `new Function`, `importScripts`, dynamic `import()`, `fetch` or `<script>` loading;
`scripting.executeScript` is only called with functions defined in `background.js`.

### Data usage: what to tick

Tick these two:

| Category | Why it applies |
|---|---|
| **Web history** | `list_tabs` returns the titles and URLs of open tabs to the agent when the user asks about their tabs, and every tab command reports the affected tab's title and URL. |
| **Website content** | `read_page` returns a page's visible text, and `find` returns the text around a match, when the user asks Jarvis about a page. |

Leave the rest unticked:

| Category | Why not |
|---|---|
| User activity | Chrome's examples are network monitoring, clicks, mouse position, scroll and keystroke logging. The extension monitors none of these. The only event it sends unprompted is "this browser window gained or lost focus" (a yes/no, no page or URL), which the agent uses to pick which browser a command goes to. It scrolls only on command and reports the new scroll position of that command. |
| Personally identifiable information, Health, Financial and payment, Authentication, Personal communications, Location | The extension doesn't look for or collect these. Form fields, passwords and editable text are never read. `fill` writes text the user dictated into a field (a password field only when named as one); the value comes from the agent, is never read back, and nothing about it leaves the browser except the field's label. A page the user asks Jarvis to read could show anything (an email, a bank statement); that is covered by "Website content". |

Why tick anything when the data only goes to the user's own server: Chrome counts data that
leaves the browser as collected, and page text and tab URLs do leave it (to the local agent,
then the user's Jarvis server). Declaring them is accurate and avoids a review rejection. The
Firefox manifest declares the same two (`browsingActivity`, `websiteContent`).

### Certifications

Tick all three:

- I do not sell or transfer user data to third parties, outside of the approved use cases.
- I do not use or transfer user data for purposes that are unrelated to my item's single purpose.
- I do not use or transfer user data to determine creditworthiness or for lending purposes.

All true: the data goes only to the user's own Jarvis server, through the agent on their own
computer, and is used only to answer the request they made. The developer never receives it.

### Test instructions (Chrome "Test instructions" tab)

Use the same text as the AMO reviewer notes below.

## AMO

### Data collection permissions

Already in the manifest, so AMO shows them on the listing and Firefox asks for consent on
install:

```json
"data_collection_permissions": {"required": ["browsingActivity", "websiteContent"]}
```

`browsingActivity` covers tab titles and URLs; `websiteContent` covers page text. Nothing is
optional. (Decision recorded in docs/agent/DESIGN.md §10, "Decisions taken", 2.)

### Source code

**AMO does not need the source code.** When the upload asks "Do you need to submit source
code?", answer **No**. Nothing in the package is minified, bundled, transpiled or
machine-generated: `background.js` is the hand-written file from the repository, byte for byte,
and the two icons are plain PNGs. The only transformation is that `jarvis-agent browser pack`
writes `manifest.json` from the shared Chrome/Firefox manifest with the Chrome-only keys
removed (`key`, `minimum_chrome_version`, `background.service_worker`). It is readable JSON, and
the reviewer notes say where its source is.

### Requires payment, non-free services or additional hardware?

Leave this **unticked**. The extension needs other software (the Jarvis agent and server), but
both are free and open source and run on hardware the user already has. The description and
the reviewer notes say plainly what's needed. If a reviewer asks for the box to be ticked, tick
it.

### Notes to reviewer (also Chrome's test instructions)

```
This extension is the browser half of Jarvis, an open-source, self-hosted voice assistant (https://github.com/alexberardi/jarvis-server, AGPL-3.0). It does nothing by itself: it only acts on commands from the Jarvis desktop agent ("jarvis-agent") on the same computer, over native messaging (host name "io.jarvis.agent.browser"). The agent in turn talks only to the user's own self-hosted Jarvis server. The extension has no UI, no content scripts, no network code, no storage, no remote code and no externally_connectable; web pages and other extensions can't message it.

WITHOUT THE AGENT (what you will see): the extension installs and stays idle. connectNative fails with "host not found" (or "Specified native messaging host not found"), and the background script retries with backoff up to once a minute (an alarm named "jarvis-reconnect" wakes it). Nothing else happens; there is nothing to click.

TO SEE IT WORK (Linux or macOS, no Jarvis server needed; about 10 minutes):
1. Build the agent: git clone https://github.com/alexberardi/jarvis-server && cd jarvis-server && scripts/fetch-sherpa-libs.sh && CGO_ENABLED=0 go build -o jarvis-agent ./cmd/jarvis-agent   (needs Go 1.27; README, "Install the agent (from source)").
2. Register the native-messaging host for your browsers: ./jarvis-agent browser install   (writes io.jarvis.agent.browser.json into each browser's per-user NativeMessagingHosts folder; ./jarvis-agent browser uninstall removes it). Then install this extension and keep the browser open.
3. Send it commands, one per line; each serves the extension for that one call and prints the result (the same commands Jarvis sends when the user speaks):
   ./jarvis-agent try browser_list_tabs
   ./jarvis-agent try browser_open '{"url":"https://example.com"}'
   ./jarvis-agent try browser_read_page
   ./jarvis-agent try browser_find '{"text":"domain"}'
   ./jarvis-agent try browser_media '{"action":"status"}'
   ./jarvis-agent try browser_fill '{"field":"search","text":"hello"}'   (on a page with a search box)
   ./jarvis-agent try browser_click '{"text":"Sign in"}'   (any button label on the open page)
   ./jarvis-agent try   (no arguments: lists the tools available once the browser connects)
A full voice or chat round trip additionally needs a self-hosted Jarvis server and "jarvis-agent enroll"; don't run "jarvis-agent run" at the same time as "try" (they share one local socket).

COMMANDS (closed set, background.js "COMMANDS"): list_tabs, open (http/https only), switch_tab, close_tab, navigate (back/forward/reload), read_page (visible text only; skips input, textarea, select, contenteditable, hidden and aria-hidden content), media (play/pause/mute/unmute/skip/restart/status on <video>/<audio>), scroll, find, fill (types the user's dictated text into the one field they named; never reads field values; a password field only when named as one), click (clicks the one visible button with the label the user said). Anything else is refused.

FILES: background.js is hand-written and unminified; it is internal/agent/browser/extension/background.js in the repository. manifest.json is generated from internal/agent/browser/extension/manifest.json (shared with the Chrome build) by "jarvis-agent browser pack --target firefox", which only removes the Chrome-only keys "key", "minimum_chrome_version" and "background.service_worker". No build tools, bundlers or minifiers are involved.
```

`jarvis-agent try` calls the same tool code the voice and chat paths use. It serves the
extension itself for that call and waits up to 10 s for the browser to connect.

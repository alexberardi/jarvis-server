// Jarvis Browser Control: runs commands from the Jarvis desktop agent on this computer.
//
// The only input is the native-messaging port to the agent's host ("io.jarvis.agent.browser"),
// which the browser starts from a manifest the agent registered. There are no content scripts,
// no runtime.onMessage / onMessageExternal / onConnectExternal listeners and no
// externally_connectable, so neither web pages nor other extensions can send this extension
// anything. Commands are a closed set (COMMANDS); anything else is refused.
//
// Page content is read without form fields: no command reads inputs, textareas, selects,
// contenteditable regions or passwords. Two commands act on a page, both on the user's spoken
// request (docs/agent/DESIGN.md §14): fill types into one field the user named (a password
// field only when they named it as one; values are written, never read back), and click
// presses one button by its visible label. Each refuses unless exactly one visible element
// matches.
//
// Computer control (0.3.0, docs/agent/DESIGN.md §10): snapshot lists a page's interactive elements
// with refs (with field values, except password, card and one-time-code fields), and act does one
// thing to the element a ref names (never typing into a password field), in a task window that
// open { background: true } creates behind the user's.
//
// Recording (record_start/record_stop): while the user records a task, every frame reports their
// clicks, field changes, submits and Enter/Escape/Tab, plus navigations and downloads, as
// structured events (and cannot_watch for a page it can't script). Secret fields' values are
// never read (masked: true instead).
"use strict";

const api = globalThis.browser ?? globalThis.chrome;
const HOST = "io.jarvis.agent.browser";
const PROTOCOL = 1;
const RETRY_MIN_MS = 2000;
const RETRY_MAX_MS = 60000;
const TITLE_MAX = 150;
const URL_MAX = 300;
const LIST_MAX = 100;

let port = null;
let connectedAt = 0;
let retryMs = RETRY_MIN_MS;
let retryTimer = null;
let lastMediaTab = null; // where media was last controlled, so "resume" finds a paused tab

function fail(code, message) {
  const e = new Error(message);
  e.code = code;
  return e;
}

function clip(s, n) {
  s = String(s ?? "");
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}

function post(p, msg) {
  try {
    p.postMessage(msg);
  } catch {
    // the port closed under us; onDisconnect reconnects
  }
}

// --- connection to the agent's native host ---

function connect() {
  if (port) return;
  clearTimeout(retryTimer);
  retryTimer = null;
  let p;
  try {
    p = api.runtime.connectNative(HOST);
  } catch {
    setState("unreachable");
    scheduleRetry();
    return;
  }
  port = p;
  connectedAt = Date.now();
  p.onMessage.addListener((msg) => onMessage(p, msg));
  p.onDisconnect.addListener(() => {
    // "host not found" / host exited: read it (so Chrome doesn't warn) for the popup's state.
    const err = api.runtime.lastError ?? p.error;
    setState(HOST_MISSING.test(String(err?.message ?? "")) ? "missing" : "unreachable");
    if (port === p) port = null;
    // A port that lived a while was a working host that went away: retry quickly.
    if (Date.now() - connectedAt > 10000) retryMs = RETRY_MIN_MS;
    scheduleRetry();
  });
  hello(p);
  // A missing host closes the port at once; one still open a moment later was found.
  setTimeout(() => {
    if (port === p) setState("connected");
  }, CONNECTED_AFTER_MS);
}

// --- the toolbar button's popup ---
//
// The popup (popup.html) says whether the agent's host answered and, when it didn't, links to
// the desktop agent's download page. It learns the state from its own URL, set here with
// action.setPopup: the popup sends this script nothing, so there is still no runtime.onMessage.

const CONNECTED_AFTER_MS = 1000;
// Chrome: "Specified native messaging host not found."; Firefox: "No such native application …".
const HOST_MISSING = /not found|no such native application/i;
let popupState = "";

function setState(state) {
  if (state === popupState || !api.action) return;
  popupState = state;
  const quiet = (r) => Promise.resolve(r).catch(() => {});
  quiet(api.action.setPopup({ popup: "popup.html?state=" + state }));
  quiet(api.action.setBadgeText({ text: state === "connected" ? "" : "!" }));
  quiet(api.action.setTitle({
    title: state === "connected" ? "Jarvis Browser Control" : "Jarvis Browser Control: the desktop agent isn't connected",
  }));
}

function scheduleRetry() {
  if (retryTimer) return;
  retryTimer = setTimeout(() => {
    retryTimer = null;
    connect();
  }, retryMs);
  retryMs = Math.min(retryMs * 2, RETRY_MAX_MS);
}

async function browserInfo() {
  if (api.runtime.getBrowserInfo) {
    const i = await api.runtime.getBrowserInfo();
    return { name: i.name, version: i.version };
  }
  const brands = globalThis.navigator?.userAgentData?.brands ?? [];
  const b = brands.find((x) => !/Chromium|Not.?A.?Brand/i.test(x.brand)) ?? brands.find((x) => /Chromium/.test(x.brand));
  return { name: b?.brand ?? "Chromium", version: b?.version ?? "" };
}

async function hello(p) {
  let info = { name: "unknown", version: "" };
  let focused = false;
  try {
    info = await browserInfo();
  } catch {}
  try {
    focused = !!(await api.windows.getLastFocused({})).focused;
  } catch {}
  post(p, {
    type: "hello",
    protocol: PROTOCOL,
    extension_version: api.runtime.getManifest().version,
    browser: info.name,
    browser_version: info.version,
    focused,
    website_access: await websiteAccess(),
  });
}

// --- website access ---
//
// The <all_urls> host permission lets the extension read and act on pages. Firefox lets the user
// turn it off (it is off after install unless they allowed it in the prompt), and then every
// page command fails and recordings keep only page loads. The agent is told in hello and on
// every change, so `jarvis-agent browser status` and the admin can say why; the popup asks for
// it back (popup.js).

const ALL_SITES = { origins: ["<all_urls>"] };

// websiteAccess is whether the extension may access every site; undefined when the browser
// can't say (hello then leaves it out).
async function websiteAccess() {
  try {
    return !!(await api.permissions.contains(ALL_SITES));
  } catch {
    return undefined;
  }
}

function accessChanged() {
  const p = port;
  if (!p) return; // the next hello carries it
  websiteAccess().then((on) => {
    if (on !== undefined && port === p) post(p, { type: "event", event: "access", website_access: on });
  });
}

api.permissions?.onAdded?.addListener(accessChanged);
api.permissions?.onRemoved?.addListener(accessChanged);

async function onMessage(p, msg) {
  if (!msg || msg.type !== "command") return;
  const id = msg.id;
  const args = msg.args && typeof msg.args === "object" ? msg.args : {};
  const fn = Object.prototype.hasOwnProperty.call(COMMANDS, msg.cmd) ? COMMANDS[msg.cmd] : null;
  if (!fn) {
    post(p, { type: "result", id, ok: false, code: "unknown_command", error: `unknown command ${clip(msg.cmd, 40)}` });
    return;
  }
  try {
    const result = await fn(args);
    post(p, { type: "result", id, ok: true, result });
  } catch (e) {
    post(p, { type: "result", id, ok: false, code: e?.code ?? "failed", error: clip(e?.message ?? e, 500) });
  }
}

// The agent targets the browser the user used last; tell it when this one gains or loses focus.
api.windows.onFocusChanged.addListener((windowId) => {
  if (port) post(port, { type: "event", event: "focus", focused: windowId !== api.windows.WINDOW_ID_NONE });
});

// --- tabs ---

function describe(t) {
  return {
    tab_id: t.id,
    window_id: t.windowId,
    title: clip(t.title, TITLE_MAX),
    url: clip(t.pendingUrl || t.url, URL_MAX),
    active: !!t.active,
    audible: !!t.audible,
    muted: !!t.mutedInfo?.muted,
  };
}

async function tabByID(raw) {
  const id = Number(raw);
  if (!Number.isInteger(id)) throw fail("invalid_argument", "tab_id must be a tab id from browser_list_tabs");
  try {
    return await api.tabs.get(id);
  } catch {
    throw fail("tab_not_found", `no tab ${clip(raw, 20)} (it may have been closed); list the tabs again`);
  }
}

// The tab a command means: the given one, else the active tab of the last focused window.
async function tabFor(args) {
  if (args.tab_id != null && args.tab_id !== "") return tabByID(args.tab_id);
  const [t] = await api.tabs.query({ active: true, lastFocusedWindow: true });
  if (t) return t;
  const [any] = await api.tabs.query({ active: true });
  if (!any) throw fail("tab_not_found", "no active tab");
  return any;
}

function checkURL(s) {
  let u;
  try {
    u = new URL(String(s ?? "").trim());
  } catch {
    throw fail("invalid_argument", "url must be a full http(s) address, e.g. https://example.com");
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") throw fail("invalid_argument", "only http and https pages can be opened");
  return u.href;
}

function clampInt(v, lo, hi, dflt) {
  const n = Math.round(Number(v));
  if (!Number.isFinite(n)) return dflt;
  return Math.min(Math.max(n, lo), hi);
}

async function inject(t, { func, args = [], allFrames = false, frameIds = null }) {
  const target = { tabId: t.id };
  if (allFrames) target.allFrames = true;
  if (frameIds) target.frameIds = frameIds;
  try {
    return await api.scripting.executeScript({ target, func, args });
  } catch (e) {
    const m = String(e?.message ?? e);
    // Browser-internal pages, extension stores, PDFs viewers, discarded tabs...
    if (/cannot (access|be scripted)|missing host permission|extensions gallery|privileged|not allowed|chrome:|about:|moz-extension:|resource:|view-source:/i.test(m + " " + (t.url ?? ""))) {
      throw fail("restricted_page", `the browser doesn't let extensions read or control this page (${clip(t.url, 80)})`);
    }
    throw fail("failed", clip(m, 300));
  }
}

async function focusWindow(windowId) {
  try {
    await api.windows.update(windowId, { focused: true });
  } catch {}
}

const MEDIA_ACTIONS = new Set(["play", "pause", "mute", "unmute", "skip_forward", "skip_back", "restart", "status"]);

const COMMANDS = {
  async list_tabs(args) {
    let focusedWin = null;
    try {
      focusedWin = (await api.windows.getLastFocused({})).id;
    } catch {}
    const q = String(args.query ?? "").toLowerCase().trim();
    const tabs = (await api.tabs.query({}))
      .filter((t) => !q || (t.title ?? "").toLowerCase().includes(q) || (t.url ?? "").toLowerCase().includes(q))
      .map((t) => ({ ...describe(t), current: !!t.active && t.windowId === focusedWin }));
    return { tabs: tabs.slice(0, LIST_MAX), count: tabs.length, truncated: tabs.length > LIST_MAX };
  },

  async open(args) {
    const url = checkURL(args.url);
    if (args.background === true) {
      // A task window behind the user's: the agent works in it without taking their focus.
      const w = await api.windows.create({ url, focused: false, state: "normal" });
      const tab = w.tabs?.[0] ?? (await api.tabs.query({ windowId: w.id }))[0];
      if (!tab) throw fail("failed", "the browser opened no tab in the new window");
      return { tab: describe(tab), window_id: w.id };
    }
    const tab = await api.tabs.create({ url, active: true });
    await focusWindow(tab.windowId);
    return { tab: describe(tab) };
  },

  async switch_tab(args) {
    if (args.tab_id == null || args.tab_id === "") throw fail("invalid_argument", "tab_id is required");
    const t = await tabByID(args.tab_id);
    await api.tabs.update(t.id, { active: true });
    await focusWindow(t.windowId);
    return { tab: describe({ ...t, active: true }) };
  },

  async close_tab(args) {
    if (args.tab_id == null || args.tab_id === "") throw fail("invalid_argument", "tab_id is required");
    const t = await tabByID(args.tab_id);
    await api.tabs.remove(t.id);
    return { closed: describe(t) };
  },

  async navigate(args) {
    const t = await tabFor(args);
    const go = { back: api.tabs.goBack, forward: api.tabs.goForward, reload: api.tabs.reload }[args.action];
    if (!go || !Object.prototype.hasOwnProperty.call({ back: 1, forward: 1, reload: 1 }, args.action)) {
      throw fail("invalid_argument", "action must be back, forward or reload");
    }
    try {
      await go.call(api.tabs, t.id);
    } catch (e) {
      if (args.action !== "reload") throw fail("no_history", `there is no page to go ${args.action} to in this tab`);
      throw e;
    }
    return { tab_id: t.id, action: args.action };
  },

  async read_page(args) {
    const t = await tabFor(args);
    const max = clampInt(args.max_chars, 500, 50000, 4000);
    const [r] = await inject(t, { func: extractText, args: [max] });
    if (!r?.result) throw fail("failed", "the page returned nothing");
    return { tab: describe(t), ...r.result };
  },

  async media(args) {
    const action = String(args.action ?? "");
    if (!MEDIA_ACTIONS.has(action)) throw fail("invalid_argument", `action must be one of ${[...MEDIA_ACTIONS].join(", ")}`);
    const tabs = await mediaCandidates(args);
    let lastErr = null;
    for (const t of tabs) {
      if (action === "mute" || action === "unmute") {
        // Tab-level mute covers every player on the page (and Web Audio).
        await api.tabs.update(t.id, { muted: action === "mute" });
        lastMediaTab = t.id;
        return { tab: describe({ ...t, mutedInfo: { muted: action === "mute" } }), action };
      }
      let probes;
      try {
        probes = await inject(t, { func: mediaProbe, allFrames: true });
      } catch (e) {
        lastErr = e;
        continue;
      }
      const withMedia = probes.filter((p) => p?.result?.count > 0);
      if (!withMedia.length) continue;
      const pick = withMedia.find((p) => p.result.playing) ?? withMedia.find((p) => p.frameId === 0) ?? withMedia[0];
      const [r] = await inject(t, { func: mediaControl, args: [action, Number(args.seconds) || 0], frameIds: [pick.frameId] });
      const res = r?.result;
      if (!res) throw fail("failed", "the page returned nothing");
      if (res.error) throw fail(res.code ?? "failed", res.error);
      lastMediaTab = t.id;
      return { tab: describe(t), action, media: res };
    }
    if (lastErr && tabs.length === 1) throw lastErr;
    throw fail("no_media", "no video or audio found in the tab" + (args.tab_id != null ? "" : "s playing, the active tab or the last controlled tab"));
  },

  async scroll(args) {
    const t = await tabFor(args);
    const dir = String(args.direction ?? "");
    if (!["up", "down", "top", "bottom"].includes(dir)) throw fail("invalid_argument", "direction must be up, down, top or bottom");
    const [r] = await inject(t, { func: scrollPage, args: [dir] });
    return { tab_id: t.id, ...(r?.result ?? {}) };
  },

  async find(args) {
    const t = await tabFor(args);
    const text = String(args.text ?? "").trim();
    if (!text) throw fail("invalid_argument", "text is required");
    const [r] = await inject(t, { func: findText, args: [clip(text, 200)] });
    return { tab: describe(t), text, ...(r?.result ?? {}) };
  },

  async fill(args) {
    let t = await tabFor(args);
    const text = String(args.text ?? "");
    if (!text.trim()) throw fail("invalid_argument", "text is required");
    if (text.length > FILL_MAX) throw fail("invalid_argument", `text is longer than ${FILL_MAX} characters`);
    const field = clip(String(args.field ?? "").trim(), 100);
    const [t2, frame] = await readyFrame(t, fieldAct, [field, "", false, false], "field");
    t = t2;
    const [r] = await inject(t, { func: fieldAct, args: [field, text, !!args.submit, true], frameIds: [frame] });
    const res = r?.result;
    if (!res) throw fail("failed", "the page returned nothing");
    if (res.error) throw fail(res.code ?? "failed", res.error);
    return { tab: describe(t), ...res };
  },

  async click(args) {
    let t = await tabFor(args);
    const label = clip(String(args.text ?? "").trim(), 100);
    if (!label) throw fail("invalid_argument", "text (the button's label) is required");
    const [t2, frame] = await readyFrame(t, buttonAct, [label, false], "button");
    t = t2;
    const [r] = await inject(t, { func: buttonAct, args: [label, true], frameIds: [frame] });
    const res = r?.result;
    if (!res) throw fail("failed", "the page returned nothing");
    if (res.error) throw fail(res.code ?? "failed", res.error);
    return { tab: describe(t), ...res };
  },

  // snapshot lists the tab's visible interactive elements, every frame's, each stamped with a
  // ref act can name. A ref is good for one snapshot (gen) of one document.
  async snapshot(args) {
    const t = await tabFor(args);
    const max = clampInt(args.max, 1, SNAPSHOT_MAX, 300);
    const gen = ++genSeq;
    const probes = (await inject(t, { func: snapCollect, args: [gen, max], allFrames: true }))
      .filter((p) => p?.result?.token)
      .sort((a, b) => a.frameId - b.frameId);
    const top = probes.find((p) => p.frameId === 0)?.result;
    const elements = [];
    const commit = {};
    const frameOf = new Map();
    for (const p of probes) {
      const offset = elements.length;
      const keep = Math.max(0, Math.min(p.result.elements.length, max - offset));
      commit[p.result.token] = [offset, keep];
      for (const e of p.result.elements.slice(0, keep)) {
        const n = offset + e.k;
        frameOf.set(n, p.frameId);
        const { k, ...rest } = e;
        elements.push({ ref: "e" + n, ...rest, frame: p.frameId });
      }
    }
    await inject(t, { func: snapCommit, args: [gen, commit], allFrames: true });
    snaps.set(t.id, { gen, frameOf });
    return { gen, url: clip(top?.url ?? t.url, URL_MAX), title: clip(top?.title ?? t.title, TITLE_MAX), elements };
  },

  // act does one op on the element a snapshot ref names. It refuses a ref from another snapshot
  // or document (stale_ref), and typing into a password field (password_field).
  async act(args) {
    const t = await tabFor(args);
    const gen = Number(args.gen);
    const op = String(args.op ?? "");
    if (!ACT_OPS.has(op)) throw fail("invalid_argument", `op must be one of ${[...ACT_OPS].join(", ")}`);
    const value = String(args["value"] ?? ""); // the agent's text, not the page's
    if (value.length > FILL_MAX) throw fail("invalid_argument", `value is longer than ${FILL_MAX} characters`);
    const keys = Array.isArray(args.keys) ? args.keys.map(String) : [];
    if (op === "keys" && (!keys.length || keys.some((k) => !ACT_KEYS.has(k)))) {
      throw fail("invalid_argument", `keys must be some of ${[...ACT_KEYS].join(", ")}`);
    }
    await actOn(t, gen, args.ref, op, value, keys);
    const after = await settled(t.id);
    return { ok: true, url_after: clip(after.url, URL_MAX), title_after: clip(after.title, TITLE_MAX) };
  },

  // record_start starts recording the user's actions in every tab (recInstall), and in each tab
  // that loads while it runs; they go to the agent as "record" events until record_stop.
  async record_start(args) {
    const id = String(args.recording_id ?? "");
    if (!REC_ID.test(id)) throw fail("invalid_argument", "recording_id must be 1-64 letters, digits, _ or -");
    if (rec && rec.id !== id) throw fail("busy", "another recording is running; stop it first");
    // Firefox lets the user take back <all_urls> (and a temporary add-on may never get it): then
    // no page can be watched and only navigations would be recorded. Asking for it needs a click
    // in the browser, so say where to turn it on.
    if (!(await api.permissions.contains({ origins: ["<all_urls>"] }))) {
      throw fail("no_site_access", api.runtime.getBrowserInfo
        ? "Jarvis Browser Control doesn't have access to websites, so it can't see what you do. In Firefox, open about:addons, then Jarvis Browser Control, Permissions, and turn on Access your data for all websites."
        : "Jarvis Browser Control doesn't have access to websites, so it can't see what you do. Open chrome://extensions, then Jarvis Browser Control, Details, and set Site access to On all sites.");
    }
    if (!rec) rec = { id, seq: 0, tabs: new Set(), polling: new Set() };
    const r = rec;
    await Promise.all((await api.tabs.query({})).map((t) => recTab(r, t)));
    return { ok: true, tabs: r.tabs.size };
  },

  // record_stop removes the listeners, sends what they still held, and says how many events the
  // recording sent.
  async record_stop(args) {
    const id = String(args.recording_id ?? "");
    if (!rec || rec.id !== id) throw fail("not_recording", `recording ${clip(id, 64)} isn't running`);
    const r = rec;
    for (const tabId of r.tabs) {
      let res = [];
      try {
        res = await api.scripting.executeScript({ target: { tabId, allFrames: true }, func: recStop, args: [r.id] });
      } catch {} // the tab is gone or can't be scripted any more
      for (const f of res ?? []) for (const e of f?.result?.events ?? []) recPost(r, tabId, f.frameId, e);
    }
    rec = null;
    return { events: r.seq };
  },

  // screenshot captures the visible page of a (task) window as a JPEG, with the viewport's CSS
  // size and device pixel ratio (the image is in physical pixels). A minimised window shows
  // nothing, so it is refused.
  async screenshot(args) {
    const id = Number(args.window_id);
    if (!Number.isInteger(id)) throw fail("invalid_argument", "window_id must be a task window's id");
    let w;
    try {
      w = await api.windows.get(id, { populate: true });
    } catch {
      throw fail("window_not_found", `no window ${clip(args.window_id, 20)} (it may have been closed)`);
    }
    if (w.state === "minimized") throw fail("window_minimized", "the task window is minimised, so it can't be captured; restore it");
    const t = w.tabs?.find((x) => x.active);
    if (!t) throw fail("tab_not_found", "the window has no active tab");
    const [v] = await inject(t, { func: viewportSize });
    if (!v?.result) throw fail("failed", "the page returned no viewport size");
    const url = await api.tabs.captureVisibleTab(id, { format: "jpeg", quality: 80 });
    const data = String(url).replace(/^data:image\/jpeg;base64,/, "");
    return { data, width: v.result.width, height: v.result.height, device_pixel_ratio: v.result.dpr };
  },

  // download saves a file into the browser's downloads folder: the one pressing ref (of the tab's
  // last snapshot) starts within DOWNLOAD_START_MS, or url's. It waits (≤ DOWNLOAD_DONE_MS) for
  // the file to complete and says where it is.
  async download(args) {
    const t = await tabFor(args);
    const byRef = args.ref != null && args.ref !== "";
    if (byRef === (args.url != null && args.url !== "")) throw fail("invalid_argument", "give either a ref or a url");
    let id;
    if (byRef) {
      const known = snaps.get(t.id);
      if (!known) throw fail("stale_ref", "take a snapshot of the tab first and use its refs");
      const created = nextDownload(DOWNLOAD_START_MS);
      try {
        await actOn(t, known.gen, args.ref, "press", "", []);
      } catch (e) {
        created.cancel();
        throw e;
      }
      id = await created.id;
    } else {
      id = await api.downloads.download({ url: checkURL(args.url), saveAs: false, conflictAction: "uniquify" });
    }
    const d = await downloadDone(id, DOWNLOAD_DONE_MS);
    const path = String(d.filename ?? "");
    return {
      path,
      bytes: d.fileSize > 0 ? d.fileSize : d.bytesReceived ?? 0,
      mime: String(d.mime ?? ""),
      filename: path.split(/[\\/]/).pop(),
    };
  },
};

const FILL_MAX = 2000;
const SNAPSHOT_MAX = 1000;
const ACT_OPS = new Set(["press", "set_value", "select", "check", "focus", "scroll_into_view", "keys"]);
const ACT_KEYS = new Set(["Enter", "Escape", "Tab", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"]);
const DOWNLOAD_START_MS = 10000;
const DOWNLOAD_DONE_MS = 60000;

// Snapshot generations: one sequence for all tabs, seeded from the clock so a restarted service
// worker doesn't hand out a gen an older snapshot already used. snaps remembers each tab's last
// snapshot (its gen and each ref's frame) while this worker lives; the page holds the rest.
let genSeq = Math.floor(Date.now() / 1000) % 1000000000;
const snaps = new Map();
api.tabs.onRemoved.addListener((id) => snaps.delete(id));

// settled waits (≤ 5 s) for the tab to finish loading after an act, and returns it.
async function settled(tabId) {
  const until = Date.now() + 5000;
  await new Promise((r) => setTimeout(r, 150)); // a click's navigation starts a moment later
  let t;
  try {
    t = await api.tabs.get(tabId);
    while (t.status === "loading" && Date.now() < until) {
      await new Promise((r) => setTimeout(r, 100));
      t = await api.tabs.get(tabId);
    }
  } catch {
    return { url: "", title: "" }; // the act closed the tab
  }
  return { url: t.pendingUrl || t.url, title: t.title };
}

// actOn does op on the element ref ("e12") of snapshot gen in tab t. It refuses a ref from
// another snapshot or document (stale_ref), and typing into a password field (password_field).
async function actOn(t, gen, ref, op, value, keys) {
  const m = /^e([1-9][0-9]{0,5})$/.exec(String(ref ?? ""));
  if (!Number.isInteger(gen) || gen <= 0 || !m) throw fail("invalid_argument", "gen and ref (e.g. e12) from a snapshot are required");
  const n = Number(m[1]);
  const known = snaps.get(t.id);
  if (known && known.gen !== gen) throw fail("stale_ref", "the page has a newer snapshot; take a snapshot and use its refs");
  const probes = (await inject(t, { func: actFind, args: [gen, n], allFrames: true })).filter((p) => p?.result);
  if (!probes.some((p) => p.result.gen === gen)) throw fail("stale_ref", "the page changed since that snapshot; take a new one");
  if (probes.some((p) => p.result.moved)) throw fail("stale_ref", `${ref} moved to another frame; take a new snapshot`);
  const holders = probes.filter((p) => p.result.found);
  if (!holders.length) throw fail("not_found", `${ref} is no longer on the page; take a new snapshot`);
  const frame = holders[0].frameId;
  const was = known?.frameOf.get(n);
  if (holders.length > 1 || (was != null && was !== frame)) {
    throw fail("stale_ref", `${ref} moved to another frame; take a new snapshot`);
  }
  const [r] = await inject(t, { func: actDo, args: [gen, n, op, value, keys], frameIds: [frame] });
  const res = r?.result;
  if (!res) throw fail("failed", "the page returned nothing");
  if (res.error) throw fail(res.code ?? "failed", res.error);
}

// nextDownload resolves to the id of the next download the browser starts, within ms.
function nextDownload(ms) {
  let listener, timer;
  const stop = () => {
    clearTimeout(timer);
    api.downloads.onCreated.removeListener(listener);
  };
  const id = new Promise((resolve, reject) => {
    listener = (d) => {
      stop();
      resolve(d.id);
    };
    timer = setTimeout(() => {
      stop();
      reject(fail("no_download", `pressing that started no download within ${ms / 1000} s`));
    }, ms);
    api.downloads.onCreated.addListener(listener);
  });
  id.catch(() => {}); // a failed press cancels it unawaited
  return { id, cancel: stop };
}

// downloadDone waits (≤ ms) for download id to complete and returns its item.
async function downloadDone(id, ms) {
  let listener, timer;
  const done = new Promise((resolve, reject) => {
    const check = async () => {
      const [d] = await api.downloads.search({ id });
      if (!d) return reject(fail("download_failed", "the download disappeared (cancelled or removed)"));
      if (d.state === "complete") resolve(d);
      else if (d.state === "interrupted") reject(fail("download_failed", `the download failed (${clip(d.error, 80)})`));
    };
    listener = (delta) => {
      if (delta.id === id && delta.state) check().catch(reject);
    };
    timer = setTimeout(() => reject(fail("timeout", `the download didn't complete within ${ms / 1000} s`)), ms);
    api.downloads.onChanged.addListener(listener);
    check().catch(reject); // it may have finished already
  });
  try {
    return await done;
  } finally {
    clearTimeout(timer);
    api.downloads.onChanged.removeListener(listener);
  }
}

// viewportSize runs in the page: its viewport in CSS pixels and device pixel ratio.
function viewportSize() {
  return { width: window.innerWidth, height: window.innerHeight, dpr: window.devicePixelRatio || 1 };
}

// uniqueFrame probes every frame of the tab with func (a dry run) and returns the frame holding
// the one element that matches best. Several equally good matches, anywhere, are refused.
async function uniqueFrame(t, func, args, what) {
  const probes = (await inject(t, { func, args, allFrames: true })).filter((p) => p?.result);
  const best = Math.max(0, ...probes.map((p) => p.result.tier));
  const hits = best > 0 ? probes.filter((p) => p.result.tier === best) : [];
  const count = hits.reduce((n, p) => n + p.result.count, 0);
  if (count === 1) return hits[0].frameId;
  if (count > 1) {
    const labels = hits.flatMap((p) => p.result.labels).slice(0, 8);
    throw fail("ambiguous", `${count} ${what}s match: ${labels.join("; ")}. Ask the user which one they mean`);
  }
  const avail = [...new Set(probes.flatMap((p) => p.result.available))].slice(0, 12);
  const err = probes.find((p) => p.result.error);
  if (err) throw fail(err.result.code ?? "failed", err.result.error);
  throw fail(`${what}_not_found`, `no visible ${what} matches` + (avail.length ? `; the page has: ${avail.join("; ")}` : ""));
}

// readyFrame is uniqueFrame for fill and click, which often come right after open (#420: a
// fill 1.4 s after opening google.com, on a tab still loading): it waits (≤ 5 s) for the tab to
// load, then gives a missing element a few more tries (≤ 2 s) while the page's script adds it.
// It returns the loaded tab and the frame; a page without the element still fails.
async function readyFrame(t, func, args, what) {
  const wait = () => new Promise((r) => setTimeout(r, 250));
  for (let i = 0; i < 20 && t.status === "loading"; i++) {
    await wait();
    t = await tabByID(t.id);
  }
  for (let i = 0; ; i++) {
    try {
      return [t, await uniqueFrame(t, func, args, what)];
    } catch (e) {
      if (e.code !== `${what}_not_found` || i >= 8) throw e;
    }
    await wait();
  }
}

// Media tabs in order of preference: the given tab; else tabs playing sound (focused window
// first), the active tab, then the tab media was last controlled in.
async function mediaCandidates(args) {
  if (args.tab_id != null && args.tab_id !== "") return [await tabByID(args.tab_id)];
  const out = [];
  const seen = new Set();
  const add = (t) => {
    if (t && !seen.has(t.id)) {
      seen.add(t.id);
      out.push(t);
    }
  };
  let focusedWin = null;
  try {
    focusedWin = (await api.windows.getLastFocused({})).id;
  } catch {}
  const audible = await api.tabs.query({ audible: true });
  audible.sort((a, b) => (b.windowId === focusedWin) - (a.windowId === focusedWin));
  audible.forEach(add);
  try {
    add(await tabFor({}));
  } catch {}
  if (lastMediaTab != null) {
    try {
      add(await api.tabs.get(lastMediaTab));
    } catch {}
  }
  if (!out.length) throw fail("tab_not_found", "no tab to control");
  return out;
}

// --- recording ---

// While a recording runs, every frame of every tab carries recInstall's listeners, which queue
// the user's actions in this extension's isolated world. Pages have no way to message the
// extension (no runtime.onMessage), so the background long-polls each frame instead: recPoll
// returns as soon as the frame has an event (or after REC_POLL_MS with none), and each event goes
// to the agent as { type: "event", event: "record", recording_id, seq, ... }. A navigation ends
// that document's recorder; the tab's next load (tabs.onUpdated complete) installs a new one.
const REC_ID = /^[A-Za-z0-9_-]{1,64}$/;
const REC_POLL_MS = 20000;
let rec = null; // { id, seq, tabs: tab ids with a recorder, polling: "tab:frame:token" keys }

function recPost(r, tabId, frameId, e) {
  r.seq++;
  const m = { type: "event", event: "record", recording_id: r.id, seq: r.seq, ts: Number(e.ts) || Date.now(), kind: String(e.kind), url: clip(e.url, URL_MAX) };
  if (tabId != null) m.tab = tabId;
  if (e.key) m.key = String(e.key);
  if (e.mime) m.mime = clip(e.mime, 100);
  if (e.element) m.element = { ...e.element, frame: frameId ?? 0 };
  if (e.masked) m.masked = true;
  else if (e["value"] != null) m["value"] = String(e["value"]);
  if (port) post(port, m);
}

// recTab installs the recorder in every frame of t and polls each new one.
async function recTab(r, t) {
  if (rec !== r || !/^https?:/.test(t.url ?? "")) return;
  let res;
  try {
    res = await inject(t, { func: recInstall, args: [r.id], allFrames: true });
  } catch {
    // A page the browser doesn't let extensions script: say so, or the trace would hold only
    // its navigations.
    if (rec === r) recPost(r, t.id, null, { kind: "cannot_watch", url: t.url });
    return;
  }
  r.tabs.add(t.id);
  for (const f of res) if (f?.result?.token) recPollFrame(r, t.id, f.frameId, f.result.token);
}

async function recPollFrame(r, tabId, frameId, token) {
  const key = `${tabId}:${frameId}:${token}`;
  if (r.polling.has(key)) return;
  r.polling.add(key);
  try {
    while (rec === r) {
      let res;
      try {
        [res] = await api.scripting.executeScript({ target: { tabId, frameIds: [frameId] }, func: recPoll, args: [r.id, token, REC_POLL_MS] });
      } catch {
        break; // the frame or tab went away
      }
      const out = res?.result;
      if (rec === r) for (const e of out?.events ?? []) recPost(r, tabId, frameId, e);
      if (!out?.alive) break;
    }
  } finally {
    r.polling.delete(key);
  }
}

api.tabs.onUpdated.addListener((tabId, info, t) => {
  const r = rec;
  if (!r) return;
  if (info.url && /^https?:/.test(info.url)) recPost(r, tabId, null, { kind: "navigate", url: info.url });
  if (info.status === "complete") recTab(r, t);
});
api.tabs.onRemoved.addListener((id) => rec?.tabs.delete(id));
// Downloads need the "downloads" permission; without it there is no api.downloads and no event.
api.downloads?.onCreated.addListener((d) => {
  if (rec) recPost(rec, null, null, { kind: "download", url: d.finalUrl || d.url, mime: d.mime });
});

// --- functions injected into pages (self-contained: they run in the page's isolated world) ---

// extractText returns the page's visible text, main content first, never form-field values.
function extractText(maxChars) {
  const SKIP = new Set(["script", "style", "noscript", "template", "svg", "canvas", "iframe", "object", "embed",
    "input", "textarea", "select", "option", "datalist", "head", "audio", "video"]);
  const BLOCK = new Set(["p", "div", "li", "ul", "ol", "h1", "h2", "h3", "h4", "h5", "h6", "tr", "table", "section",
    "article", "main", "header", "footer", "nav", "aside", "blockquote", "pre", "figure", "figcaption", "dt", "dd",
    "dl", "form", "fieldset", "details", "summary", "hr", "br", "td", "th"]);
  let root = document.body;
  let rootName = "body";
  for (const s of ["main", "[role=main]", "article"]) {
    const el = document.querySelector(s);
    if (el && (el.textContent || "").trim().length >= 500) {
      root = el;
      rootName = s === "[role=main]" ? "main" : s;
      break;
    }
  }
  if (!root) return { title: document.title, text: "", truncated: false, root: "none" };
  const visible = (el) => (typeof el.checkVisibility === "function" ? el.checkVisibility({ visibilityProperty: true }) : true);
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      if (node.nodeType === Node.TEXT_NODE) return NodeFilter.FILTER_ACCEPT;
      const el = node;
      if (SKIP.has(el.localName) || el.isContentEditable || el.hidden || el.getAttribute("aria-hidden") === "true" || !visible(el)) {
        return NodeFilter.FILTER_REJECT;
      }
      return NodeFilter.FILTER_ACCEPT;
    },
  });
  const parts = [];
  let n = 0;
  let truncated = false;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.nodeType === Node.TEXT_NODE) {
      const s = node.nodeValue.replace(/\s+/g, " ");
      if (!s) continue;
      parts.push(s);
      n += s.length;
    } else if (BLOCK.has(node.localName)) {
      parts.push("\n");
    }
    if (n > maxChars + 200) {
      truncated = true;
      break;
    }
  }
  let text = parts.join("").replace(/ *\n\s*/g, "\n").replace(/ {2,}/g, " ").trim();
  if (text.length > maxChars) {
    text = text.slice(0, maxChars);
    truncated = true;
  }
  return { title: document.title, text, truncated, root: rootName };
}

function mediaProbe() {
  const els = [...document.querySelectorAll("video, audio")];
  return { count: els.length, playing: els.some((e) => !e.paused && !e.ended) };
}

async function mediaControl(action, seconds) {
  const els = [...document.querySelectorAll("video, audio")];
  const area = (e) => (e.localName === "video" ? e.clientWidth * e.clientHeight : 0);
  const el = els.find((e) => !e.paused && !e.ended) ??
    els.filter((e) => e.readyState > 0 || e.currentSrc).sort((a, b) => area(b) - area(a))[0] ?? els[0];
  if (!el) return { error: "no video or audio here", code: "no_media" };
  const step = Number.isFinite(seconds) && seconds > 0 ? seconds : 10;
  try {
    switch (action) {
      case "play":
        await el.play();
        break;
      case "pause":
        el.pause();
        break;
      case "skip_forward":
        el.currentTime = Number.isFinite(el.duration) ? Math.min(el.currentTime + step, el.duration) : el.currentTime + step;
        break;
      case "skip_back":
        el.currentTime = Math.max(el.currentTime - step, 0);
        break;
      case "restart":
        el.currentTime = 0;
        break;
    }
  } catch (e) {
    if (e && e.name === "NotAllowedError") {
      return { error: "the browser blocked playback that wasn't started by a click; press play in the browser once", code: "blocked" };
    }
    return { error: String((e && e.message) || e), code: "failed" };
  }
  return {
    kind: el.localName,
    paused: el.paused,
    muted: el.muted,
    position_seconds: Math.round(el.currentTime),
    duration_seconds: Number.isFinite(el.duration) ? Math.round(el.duration) : null,
  };
}

function scrollPage(direction) {
  // The page itself, or (web apps that scroll an inner pane) its largest scrollable element.
  let el = document.scrollingElement || document.documentElement;
  if (el.scrollHeight <= el.clientHeight + 10) {
    let best = null;
    const all = document.body ? document.body.getElementsByTagName("*") : [];
    for (let i = 0; i < all.length && i < 5000; i++) {
      const c = all[i];
      if (c.scrollHeight > c.clientHeight + 10 && c.clientHeight > 100) {
        const oy = getComputedStyle(c).overflowY;
        if ((oy === "auto" || oy === "scroll") && (!best || c.clientHeight > best.clientHeight)) best = c;
      }
    }
    if (best) el = best;
  }
  const page = el.clientHeight * 0.85;
  const top = { up: el.scrollTop - page, down: el.scrollTop + page, top: 0, bottom: el.scrollHeight }[direction];
  el.scrollTo({ top, behavior: "instant" });
  const max = Math.max(el.scrollHeight - el.clientHeight, 1);
  return { direction, position_percent: Math.round((Math.min(Math.max(top, 0), max) / max) * 100) };
}

function findText(text) {
  const sel = window.getSelection();
  if (sel) sel.removeAllRanges();
  // window.find highlights the first match and scrolls it into view (Firefox and Chromium).
  const found = typeof window.find === "function" ? window.find(text, false, false, true, false, false, false) : false;
  const hay = (document.body ? document.body.innerText : "") || "";
  const lower = hay.toLowerCase();
  const needle = text.toLowerCase();
  let matches = 0;
  let first = -1;
  for (let i = lower.indexOf(needle); i !== -1 && matches < 1000; i = lower.indexOf(needle, i + needle.length)) {
    if (first < 0) first = i;
    matches++;
  }
  const context = first < 0 ? "" : hay.slice(Math.max(0, first - 150), first + needle.length + 150).replace(/\s+/g, " ").trim();
  return { found: found || matches > 0, matches, context };
}

// fieldAct finds the one editable field `query` names (its label, placeholder, name or
// autocomplete purpose; empty: the focused field) and, with act, types `text` into it,
// replacing what was there, then submits its form if asked. Values are written, never read. A
// password field counts only when the query says password (or pin, passcode).
function fieldAct(query, text, submit, act) {
  const norm = (s) => String(s ?? "").toLowerCase().replace(/[^\p{L}\p{N}]+/gu, " ").trim();
  let q = norm(query);
  for (let prev = ""; prev !== q; ) {
    prev = q;
    q = q.replace(/^(the|my|a|an) /, "").replace(/ (field|box|input|text ?box|area|textarea|bar)$/, "").trim();
  }
  const wantPw = /\b(password|passcode|passphrase|pin|pwd)\b/.test(q);
  const SYN = {
    username: ["user", "user name", "login", "login name", "user id", "userid", "email", "e mail", "email address", "account", "username or email", "email or username", "sign in"],
    email: ["e mail", "email address", "mail", "username or email", "email or username"],
    password: ["passcode", "pwd", "pass", "current password", "your password"],
    search: ["q", "query", "find", "search for", "keywords"],
    phone: ["telephone", "tel", "mobile", "phone number", "cell"],
    name: ["full name", "your name"],
    message: ["comment", "comments", "body", "text"],
  };
  const AUTO = { username: ["username", "email"], email: ["email"], password: ["current password", "new password"], phone: ["tel"], name: ["name"], search: [] };
  const TEXTY = new Set(["", "text", "email", "search", "tel", "url", "password", "number"]);
  const typeOf = (el) => (el.getAttribute("type") || "").toLowerCase();
  const isPw = (el) => el.localName === "input" && typeOf(el) === "password";
  const visible = (el) => (typeof el.checkVisibility === "function" ? el.checkVisibility({ visibilityProperty: true }) : true) && el.getClientRects().length > 0;
  const editable = (el) => {
    if (el.disabled || el.readOnly || el.getAttribute("aria-disabled") === "true") return false;
    if (el.localName === "textarea") return true;
    if (el.localName === "input") return TEXTY.has(typeOf(el));
    return el.isContentEditable && !(el.parentElement && el.parentElement.isContentEditable);
  };
  const textOf = (id) => {
    const e = document.getElementById(id);
    return e ? e.textContent : "";
  };
  const labelsOf = (el) => {
    // What a person reads first (the first one names the field in results), then the markup's names.
    const out = [el.getAttribute("aria-label")];
    for (const id of (el.getAttribute("aria-labelledby") || "").split(/\s+/)) if (id) out.push(textOf(id));
    if (el.labels) for (const l of el.labels) out.push(l.textContent);
    const wrap = el.closest("label");
    if (wrap) out.push(wrap.textContent);
    out.push(el.getAttribute("placeholder"), el.getAttribute("title"), el.getAttribute("name"), el.id);
    return out.map(norm).filter(Boolean);
  };
  const describeEl = (el) => {
    const l = labelsOf(el)[0] || el.localName;
    return (isPw(el) ? "password: " : "") + (l.length > 60 ? l.slice(0, 59) + "…" : l);
  };
  const score = (el) => {
    const ls = labelsOf(el);
    const auto = norm(el.getAttribute("autocomplete"));
    if (ls.includes(q)) return 3;
    const syn = SYN[q] || [];
    if (ls.some((l) => syn.includes(l)) || (AUTO[q] || []).some((a) => auto === a) || (isPw(el) && q === "password")) return 2;
    const words = q.split(" ");
    if (ls.some((l) => words.every((w) => (" " + l + " ").includes(" " + w + " ") || (w.length >= 4 && l.includes(w))))) return 1;
    return 0;
  };
  const all = [...document.querySelectorAll("input, textarea, [contenteditable]")].filter((el) => editable(el) && visible(el));
  let pick = [];
  let tier = 0;
  if (!q) {
    const a = document.activeElement;
    if (a && all.includes(a)) {
      if (isPw(a)) return { tier: 0, count: 0, labels: [], available: [], error: "the focused field is a password field: say it is the password field to type into it", code: "password_field" };
      pick = [a];
      tier = 4;
    }
  } else {
    for (const el of all) {
      if (isPw(el) && !wantPw) continue; // a password field only when named as one
      const s = score(el);
      if (s > tier) {
        tier = s;
        pick = [el];
      } else if (s === tier && s > 0) {
        pick.push(el);
      }
    }
  }
  if (!act) {
    return {
      tier,
      count: pick.length,
      labels: pick.slice(0, 6).map(describeEl),
      available: tier ? [] : all.filter((el) => !isPw(el) || wantPw).slice(0, 12).map(describeEl),
    };
  }
  if (pick.length !== 1) return { error: "the field changed on the page; try again", code: "field_not_found" };
  const el = pick[0];
  el.focus();
  if (el.isContentEditable) {
    const r = document.createRange();
    r.selectNodeContents(el);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(r);
  } else {
    el.select();
  }
  let ok = false;
  try {
    ok = document.execCommand("insertText", false, text); // fires the input events frameworks listen to
  } catch {}
  if (!ok) {
    if (el.isContentEditable) {
      el.textContent = text;
    } else {
      const proto = el.localName === "textarea" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(proto, "value").set.call(el, text);
    }
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
  }
  let submitted = false;
  if (submit) {
    const form = el.form || el.closest("form");
    if (form && typeof form.requestSubmit === "function") {
      form.requestSubmit();
      submitted = "form";
    } else {
      for (const type of ["keydown", "keypress", "keyup"]) {
        el.dispatchEvent(new KeyboardEvent(type, { key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true, cancelable: true }));
      }
      submitted = "enter_key";
    }
  }
  return { field: describeEl(el), password: isPw(el), chars: text.length, submitted };
}

// buttonAct finds the one visible button (or link) whose label is `query` and, with act, clicks
// it. Buttons in an open dialog win over the rest of the page ("click OK" means the dialog's).
function buttonAct(query, act) {
  const norm = (s) => String(s ?? "").toLowerCase().replace(/[^\p{L}\p{N}]+/gu, " ").trim();
  let q = norm(query);
  for (let prev = ""; prev !== q; ) {
    prev = q;
    q = q.replace(/^(the|a|an) /, "").replace(/ (button|link|tab)$/, "").trim();
  }
  if (q === "okay") q = "ok";
  const SEL = "button, input[type=button], input[type=submit], input[type=reset], input[type=image], [role=button], [role=menuitem], [role=tab], [role=link], a[href], summary";
  const visible = (el) => (typeof el.checkVisibility === "function" ? el.checkVisibility({ visibilityProperty: true }) : true) && el.getClientRects().length > 0;
  const enabled = (el) => !el.disabled && el.getAttribute("aria-disabled") !== "true";
  const labelOf = (el) => {
    let l = norm(el.getAttribute("aria-label") || el.textContent || el.getAttribute("value") || el.getAttribute("title") || el.getAttribute("alt"));
    if (l === "okay") l = "ok";
    return l;
  };
  const DIALOG = "dialog[open], [role=dialog], [role=alertdialog], [aria-modal=true]";
  const all = [...document.querySelectorAll(SEL)].filter((el) => visible(el) && enabled(el));
  const score = (l) => {
    if (!l || !q) return 0;
    if (l === q) return 3;
    if (l.startsWith(q + " ") || l.endsWith(" " + q)) return 2;
    const words = q.split(" ");
    return words.every((w) => (" " + l + " ").includes(" " + w + " ")) ? 1 : 0;
  };
  let pick = [];
  let tier = 0;
  for (const el of all) {
    const s = score(labelOf(el));
    if (s > tier) {
      tier = s;
      pick = [el];
    } else if (s === tier && s > 0) {
      pick.push(el);
    }
  }
  const inDialog = pick.filter((el) => el.closest(DIALOG));
  if (inDialog.length && inDialog.length < pick.length) pick = inDialog;
  const short = (l) => (l.length > 50 ? l.slice(0, 49) + "…" : l);
  if (!act) {
    return {
      tier,
      count: pick.length,
      labels: pick.slice(0, 6).map((el) => short(labelOf(el))),
      available: tier ? [] : [...new Set(all.map(labelOf).filter(Boolean))].slice(0, 12).map(short),
    };
  }
  if (pick.length !== 1) return { error: "the button changed on the page; try again", code: "button_not_found" };
  const el = pick[0];
  const label = labelOf(el);
  el.scrollIntoView({ block: "center", behavior: "instant" });
  el.click();
  return { clicked: short(label), in_dialog: !!el.closest(DIALOG) };
}

// snapCollect lists this frame's visible interactive elements (the snapshot command; snapCommit
// then stamps the ones kept). Names follow what a person reads: aria-labelledby, aria-label, the
// <label>, placeholder, title, then the text. Values are left out for password, card and
// one-time-code fields. The elements stay in this extension's isolated world (__jarvisSnap), out
// of the page's reach, so act finds the very element even if the page copies a ref attribute.
function snapCollect(gen, max) {
  const NAME_MAX = 120;
  const SEL = "button, input, select, textarea, [contenteditable], a[href], summary, [role]";
  const ROLES = new Set(["button", "link", "checkbox", "radio", "switch", "tab", "menuitem", "menuitemcheckbox",
    "menuitemradio", "option", "combobox", "textbox", "searchbox", "listbox", "slider", "spinbutton", "treeitem"]);
  const INPUT_ROLE = { checkbox: "checkbox", radio: "radio", range: "slider", number: "spinbutton", search: "searchbox",
    button: "button", submit: "button", reset: "button", image: "button", file: "button", color: "button" };
  const clean = (s) => String(s ?? "").replace(/\s+/g, " ").trim();
  const short = (s) => {
    s = clean(s);
    return s.length > NAME_MAX ? s.slice(0, NAME_MAX - 1) + "…" : s;
  };
  const visible = (el) => (typeof el.checkVisibility === "function" ? el.checkVisibility({ visibilityProperty: true }) : true) && el.getClientRects().length > 0;
  const typeOf = (el) => (el.localName === "input" ? (el.getAttribute("type") || "text").toLowerCase() : el.localName);
  const isField = (el) => ["input", "select", "textarea"].includes(el.localName) || el.isContentEditable;
  const textOf = (id) => {
    const e = document.getElementById(id);
    return e ? e.textContent : "";
  };
  const roleOf = (el) => {
    const r = (el.getAttribute("role") || "").trim().split(/\s+/)[0].toLowerCase();
    if (ROLES.has(r)) return r;
    switch (el.localName) {
      case "a":
        return el.hasAttribute("href") ? "link" : "";
      case "button":
      case "summary":
        return "button";
      case "select":
        return el.multiple || el.size > 1 ? "listbox" : "combobox";
      case "textarea":
        return "textbox";
      case "input": {
        const t = typeOf(el);
        return t === "hidden" ? "" : INPUT_ROLE[t] ?? "textbox";
      }
    }
    return el.isContentEditable && !(el.parentElement && el.parentElement.isContentEditable) ? "textbox" : "";
  };
  const nameOf = (el) => {
    const by = (el.getAttribute("aria-labelledby") || "").split(/\s+/).filter(Boolean).map(textOf).join(" ");
    const out = [by, el.getAttribute("aria-label")];
    if (el.labels) for (const l of el.labels) out.push(l.textContent);
    const wrap = el.closest("label");
    if (wrap) out.push(wrap.textContent);
    out.push(el.getAttribute("placeholder"), el.getAttribute("title"));
    if (!isField(el)) out.push(el.innerText ?? el.textContent, el.querySelector("img[alt]")?.getAttribute("alt"));
    if (el.localName === "input" && INPUT_ROLE[typeOf(el)] === "button") out.push(el.value, el.getAttribute("alt"));
    return short(out.map(clean).find(Boolean) || "");
  };
  const secret = (el) => {
    const t = typeOf(el);
    const auto = (el.getAttribute("autocomplete") || "").toLowerCase().split(/\s+/);
    return t === "password" || t === "hidden" || auto.some((a) => a.startsWith("cc-") || a === "one-time-code");
  };
  const valueOf = (el) => {
    if (secret(el)) return undefined;
    if (el.localName === "select") return short([...el.selectedOptions].map((o) => o.label || o.text).join(", "));
    if (el.localName === "textarea") return short(el.value);
    if (el.localName === "input") {
      const t = typeOf(el);
      return t === "checkbox" || t === "radio" || INPUT_ROLE[t] === "button" ? undefined : short(el.value);
    }
    if (el.isContentEditable) return short(el.innerText);
    return undefined;
  };
  const stateOf = (el) => {
    const a = (n) => el.getAttribute(n) === "true";
    const s = [];
    if ((el.localName === "input" && el.checked) || a("aria-checked")) s.push("checked");
    if (el.disabled || a("aria-disabled")) s.push("disabled");
    if (a("aria-expanded") || (el.localName === "summary" && el.parentElement?.open)) s.push("expanded");
    if (a("aria-selected") || (el.localName === "option" && el.selected)) s.push("selected");
    if (el.required || a("aria-required")) s.push("required");
    return s;
  };

  for (const el of document.querySelectorAll("[data-jarvis-ref]")) el.removeAttribute("data-jarvis-ref");
  const pending = [];
  const elements = [];
  for (const el of document.querySelectorAll(SEL)) {
    if (pending.length >= max) break;
    const role = roleOf(el);
    if (!role || !visible(el) || el.closest("[aria-hidden=true], [inert]")) continue;
    pending.push(el);
    const e = { k: pending.length, role, name: nameOf(el), type: typeOf(el), autocomplete: clean(el.getAttribute("autocomplete")), state: stateOf(el) };
    const v = valueOf(el);
    if (v) e.value = v;
    elements.push(e);
  }
  const token = Math.random().toString(36).slice(2) + Date.now().toString(36);
  globalThis.__jarvisSnap = { gen, token, pending, refs: null };
  return { token, url: location.href, title: document.title, elements };
}

// snapCommit stamps the elements the snapshot kept with data-jarvis-ref="<gen>.<n>"; commit maps
// each frame's token to its [offset, keep].
function snapCommit(gen, commit) {
  const s = globalThis.__jarvisSnap;
  if (!s || s.gen !== gen || !s.pending) return { ok: false };
  const [offset, keep] = commit[s.token] || [0, 0];
  const refs = new Map();
  s.pending.forEach((el, i) => {
    if (i >= keep) return;
    el.setAttribute("data-jarvis-ref", gen + "." + (offset + i + 1));
    refs.set(offset + i + 1, el);
  });
  s.pending = null;
  s.refs = refs;
  return { ok: true, count: refs.size };
}

// actFind says whether this frame holds ref n of snapshot gen (and this frame's gen). Refs live
// only in this extension's isolated world: a new document has none (stale_ref), and data-jarvis-*
// attributes, which the page can write too, never pick the element.
function actFind(gen, n) {
  const s = globalThis.__jarvisSnap;
  const g = s?.refs ? s.gen : 0;
  if (g !== gen) return { gen: g, found: false };
  const el = s.refs.get(n);
  return { gen: g, found: !!el && el.isConnected && el.ownerDocument === document, moved: !!el && el.ownerDocument !== document };
}

// actDo does op on ref n of snapshot gen in this frame. Typing goes through execCommand so
// frameworks see input events, with the native value setter as the fallback (fieldAct's writer).
function actDo(gen, n, op, value, keys) {
  const s = globalThis.__jarvisSnap;
  const el = s?.refs && s.gen === gen ? s.refs.get(n) : null;
  if (!el || !el.isConnected || el.ownerDocument !== document) return { error: "the element is no longer on the page; take a new snapshot", code: "stale_ref" };
  const TEXTY = new Set(["", "text", "email", "search", "tel", "url", "password", "number"]);
  const DATEY = new Set(["date", "time", "datetime-local", "month", "week"]);
  const KEY_CODES = { Enter: 13, Escape: 27, Tab: 9, ArrowLeft: 37, ArrowUp: 38, ArrowRight: 39, ArrowDown: 40 };
  const norm = (x) => String(x ?? "").toLowerCase().replace(/\s+/g, " ").trim();
  const type = el.localName === "input" ? (el.getAttribute("type") || "").toLowerCase() : "";
  const role = (el.getAttribute("role") || "").toLowerCase();
  if ((el.disabled || el.getAttribute("aria-disabled") === "true") && op !== "focus" && op !== "scroll_into_view") {
    return { error: "that element is disabled", code: "disabled" };
  }
  el.scrollIntoView({ block: "center", behavior: "instant" });
  switch (op) {
    case "press":
      el.click();
      break;
    case "focus":
      el.focus();
      break;
    case "scroll_into_view":
      break;
    case "set_value": {
      if (el.localName === "input" && type === "password") {
        return { error: "that is a password field: Jarvis doesn't type passwords there; the user enters it", code: "password_field" };
      }
      const dateLike = el.localName === "input" && DATEY.has(type);
      const editable = el.localName === "textarea" || (el.localName === "input" && TEXTY.has(type)) || el.isContentEditable || dateLike;
      if (!editable || el.readOnly) return { error: "that element isn't a text field", code: "invalid_argument" };
      el.focus();
      if (dateLike) {
        // No typing into a date picker's segments: its value is set whole ("2026-11-01").
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(el, value);
        el.dispatchEvent(new Event("input", { bubbles: true }));
        el.dispatchEvent(new Event("change", { bubbles: true }));
        break;
      }
      if (el.isContentEditable) {
        const r = document.createRange();
        r.selectNodeContents(el);
        const sel = window.getSelection();
        sel.removeAllRanges();
        sel.addRange(r);
      } else {
        el.select();
      }
      let ok = false;
      try {
        ok = value ? document.execCommand("insertText", false, value) : document.execCommand("delete", false);
      } catch {}
      if (!ok) {
        if (el.isContentEditable) {
          el.textContent = value;
        } else {
          const proto = el.localName === "textarea" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
          Object.getOwnPropertyDescriptor(proto, "value").set.call(el, value);
        }
        el.dispatchEvent(new Event("input", { bubbles: true }));
        el.dispatchEvent(new Event("change", { bubbles: true }));
      }
      break;
    }
    case "select": {
      if (el.localName !== "select") return { error: "select works on a drop-down list; press the option instead", code: "invalid_argument" };
      const want = norm(value);
      const opts = [...el.options];
      const o = opts.find((x) => norm(x.label || x.text) === want) ?? opts.find((x) => norm(x.getAttribute("value")) === want);
      if (!o) {
        const have = opts.slice(0, 20).map((x) => (x.label || x.text).trim()).join("; ");
        return { error: `no option ${JSON.stringify(String(value).slice(0, 60))}; the options are: ${have}`, code: "not_found" };
      }
      if (o.disabled) return { error: "that option is disabled", code: "disabled" };
      el.focus();
      o.selected = true; // in a single-choice list, unselects the rest
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
      break;
    }
    case "check": {
      const box = type === "checkbox" || type === "radio";
      if (!box && !["checkbox", "radio", "switch", "menuitemcheckbox", "menuitemradio"].includes(role)) {
        return { error: "check works on a checkbox, radio button or switch", code: "invalid_argument" };
      }
      const want = !/^(false|off|0|no|unchecked)$/i.test(String(value).trim());
      const now = box ? el.checked : el.getAttribute("aria-checked") === "true";
      if (now !== want) {
        if (!want && (type === "radio" || role === "radio" || role === "menuitemradio")) {
          return { error: "a radio button can't be unchecked; check another one", code: "invalid_argument" };
        }
        el.click();
      }
      break;
    }
    case "keys": {
      el.focus();
      for (const k of keys) {
        const target = document.activeElement || el;
        const init = { key: k, code: k, keyCode: KEY_CODES[k], which: KEY_CODES[k], bubbles: true, cancelable: true };
        target.dispatchEvent(new KeyboardEvent("keydown", init));
        if (k === "Enter") target.dispatchEvent(new KeyboardEvent("keypress", init));
        target.dispatchEvent(new KeyboardEvent("keyup", init));
      }
      break;
    }
    default:
      return { error: "unknown op", code: "invalid_argument" };
  }
  return { done: op };
}

// recInstall starts recording this frame's user actions for recording rid; installing it again
// in the same document keeps the first. Elements are described as snapCollect names them (role,
// name), with a selector: a stable id, then data-testid, then name, then an aria path. Typing is
// reported once, with the final value, when the user stops (or moves on). Values are never read
// from password, hidden, card, one-time-code or password-manager fields, nor from a field
// labelled as a card, CVV, SSN or PIN: those events say masked: true instead. Only the user's own
// (trusted) events count. The queue lives in this extension's isolated world (__jarvisRec), out
// of the page's reach, until recPoll collects it.
function recInstall(rid) {
  const old = globalThis.__jarvisRec;
  if (old && old.rid === rid) return { token: old.token };
  if (old) old.off();
  const NAME_MAX = 120;
  const REC_VALUE_MAX = 500;
  const SEL_MAX = 200;
  const QUEUE_MAX = 1000;
  const TYPING_IDLE_MS = 1000;
  const REC_SECRET_LABEL = /card|cvv|cvc|csc|ssn|social security|security code|(^|[^a-z])pin([^a-z]|$)/i;
  const INTERACTIVE = "button, input, select, textarea, [contenteditable], a[href], summary, [role]";
  const ROLES = new Set(["button", "link", "checkbox", "radio", "switch", "tab", "menuitem", "menuitemcheckbox",
    "menuitemradio", "option", "combobox", "textbox", "searchbox", "listbox", "slider", "spinbutton", "treeitem"]);
  const INPUT_ROLE = { checkbox: "checkbox", radio: "radio", range: "slider", number: "spinbutton", search: "searchbox",
    button: "button", submit: "button", reset: "button", image: "button", file: "button", color: "button" };
  const TEXTY = new Set(["text", "email", "search", "tel", "url", "password", "number"]);
  const clean = (s) => String(s ?? "").replace(/\s+/g, " ").trim();
  const cut = (s, n) => (s.length > n ? s.slice(0, n - 1) + "…" : s);
  const short = (s) => cut(clean(s), NAME_MAX);
  const typeOf = (el) => (el.localName === "input" ? (el.getAttribute("type") || "text").toLowerCase() : el.localName);
  const isField = (el) => ["input", "select", "textarea"].includes(el.localName) || el.isContentEditable;
  const textLike = (el) => el.localName === "textarea" || el.isContentEditable || (el.localName === "input" && TEXTY.has(typeOf(el)));
  const textOf = (id) => {
    const e = document.getElementById(id);
    return e ? e.textContent : "";
  };
  const roleOf = (el) => {
    const r = (el.getAttribute("role") || "").trim().split(/\s+/)[0].toLowerCase();
    if (ROLES.has(r)) return r;
    switch (el.localName) {
      case "a":
        return el.hasAttribute("href") ? "link" : "";
      case "button":
      case "summary":
        return "button";
      case "select":
        return el.multiple || el.size > 1 ? "listbox" : "combobox";
      case "textarea":
        return "textbox";
      case "form":
        return "form";
      case "input": {
        const t = typeOf(el);
        return t === "hidden" ? "" : INPUT_ROLE[t] ?? "textbox";
      }
    }
    return el.isContentEditable && !(el.parentElement && el.parentElement.isContentEditable) ? "textbox" : "";
  };
  const nameOf = (el) => {
    if (el.localName === "form") return short(el.getAttribute("aria-label") || el.getAttribute("name") || el.getAttribute("id"));
    const by = (el.getAttribute("aria-labelledby") || "").split(/\s+/).filter(Boolean).map(textOf).join(" ");
    const out = [by, el.getAttribute("aria-label")];
    if (el.labels) for (const l of el.labels) out.push(l.textContent);
    const wrap = el.closest("label");
    if (wrap) out.push(wrap.textContent);
    out.push(el.getAttribute("placeholder"), el.getAttribute("title"));
    if (!isField(el)) out.push(el.innerText ?? el.textContent, el.querySelector("img[alt]")?.getAttribute("alt"));
    if (el.localName === "input" && INPUT_ROLE[typeOf(el)] === "button") out.push(el.value, el.getAttribute("alt"));
    return short(out.map(clean).find(Boolean) || "");
  };
  const secret = (el) => {
    const t = typeOf(el);
    const auto = (el.getAttribute("autocomplete") || "").toLowerCase().split(/\s+/);
    const label = [nameOf(el), el.getAttribute("name"), el.getAttribute("id")].join(" ").replace(/([a-z])([A-Z])/g, "$1 $2");
    return t === "password" || t === "hidden" || REC_SECRET_LABEL.test(label) ||
      auto.some((a) => a.startsWith("cc-") || a === "one-time-code" || a === "current-password" || a === "new-password");
  };
  const valueOf = (el) => {
    if (secret(el)) return undefined;
    if (el.localName === "select") return cut([...el.selectedOptions].map((o) => o.label || o.text).join(", "), REC_VALUE_MAX);
    if (el.localName === "textarea") return cut(el.value, REC_VALUE_MAX);
    if (el.localName === "input") {
      const t = typeOf(el);
      if (t === "checkbox" || t === "radio") return String(el.checked);
      if (t === "file") return cut([...(el.files ?? [])].map((f) => f.name).join(", "), REC_VALUE_MAX);
      return INPUT_ROLE[t] === "button" ? undefined : cut(el.value, REC_VALUE_MAX);
    }
    if (el.isContentEditable) return cut(el.innerText, REC_VALUE_MAX);
    return undefined;
  };
  const quote = (s) => JSON.stringify(String(s));
  const selectorOf = (el) => {
    const id = el.getAttribute("id");
    // Generated ids (react's ":r1:", "ember123") change between visits.
    if (id && !/\d{3,}|:/.test(id) && document.querySelectorAll("#" + CSS.escape(id)).length === 1) return "#" + CSS.escape(id);
    const tid = el.getAttribute("data-testid");
    if (tid) return cut(`[data-testid=${quote(tid)}]`, SEL_MAX);
    const nm = el.getAttribute("name");
    if (nm && el.localName !== "a") return cut(`${el.localName}[name=${quote(nm)}]`, SEL_MAX);
    const action = el.localName === "form" && el.getAttribute("action");
    if (action) return cut(`form[action=${quote(action)}]`, SEL_MAX);
    const path = [];
    for (let a = el.parentElement; a && path.length < 2; a = a.parentElement) {
      const label = clean(a.getAttribute("aria-label"));
      if (label) path.unshift(`${a.getAttribute("role") || a.localName} ${quote(cut(label, 40))}`);
    }
    path.push(`${roleOf(el) || el.localName} ${quote(cut(nameOf(el), 60))}`);
    return cut(path.join(" > "), SEL_MAX);
  };
  const describe = (el) => ({ role: roleOf(el), name: nameOf(el), tag: el.localName, type: typeOf(el),
    autocomplete: clean(el.getAttribute("autocomplete")), selector: selectorOf(el) });
  const targetOf = (ev) => {
    let n = ev.composedPath?.()[0] ?? ev.target;
    while (n && n.nodeType !== 1) n = n.parentNode;
    return n;
  };

  const queue = [];
  let waiter = null;
  let typing = null; // { el, timer }: the field being typed in
  const wake = () => {
    const w = waiter;
    waiter = null;
    if (w) w();
  };
  const record = (kind, el, extra, withValue) => {
    if (queue.length >= QUEUE_MAX) return;
    const e = { kind, ts: Date.now(), url: location.href, ...extra };
    if (el) {
      e.element = describe(el);
      if (withValue && secret(el)) {
        e.masked = true;
      } else if (withValue) {
        const v = valueOf(el);
        if (v !== undefined) e["value"] = v;
      }
    }
    queue.push(e);
    wake();
  };
  const flushTyping = () => {
    if (!typing) return;
    clearTimeout(typing.timer);
    const el = typing.el;
    typing = null;
    record("input", el, {}, true);
  };
  const on = {
    click(el) {
      el = el.closest(INTERACTIVE) || el;
      if (typing && typing.el !== el) flushTyping();
      record("click", el, {}, false);
    },
    input(el) {
      if (!textLike(el)) return;
      if (typing && typing.el !== el) flushTyping();
      typing ??= { el };
      clearTimeout(typing.timer);
      typing.timer = setTimeout(flushTyping, TYPING_IDLE_MS);
    },
    change(el) {
      if (textLike(el)) {
        if (typing?.el === el) flushTyping(); // input reports text fields
        return;
      }
      flushTyping();
      record("change", el, {}, true);
    },
    submit(el) {
      flushTyping();
      record("submit", el, {}, false);
    },
    keydown(el, ev) {
      if (ev.key !== "Enter" && ev.key !== "Escape" && ev.key !== "Tab") return;
      flushTyping();
      record("key", el, { key: ev.key }, false);
    },
  };
  const listeners = Object.entries(on).map(([type, h]) => [type, (ev) => {
    if (!ev.isTrusted) return;
    const el = targetOf(ev);
    if (!el) return;
    try {
      h(el, ev);
    } catch {}
  }]);
  listeners.push(["pagehide", () => flushTyping()]);
  for (const [type, fn] of listeners) addEventListener(type, fn, true);

  const state = {
    rid,
    token: Math.random().toString(36).slice(2) + Date.now().toString(36),
    // poll returns the queued events, waiting up to waitMs for one; alive is false once stopped.
    poll(waitMs) {
      wake(); // one poll at a time: an older one returns now
      if (queue.length) return { alive: true, events: queue.splice(0) };
      return new Promise((resolve) => {
        const fire = () => {
          clearTimeout(timer);
          if (waiter === fire) waiter = null;
          resolve({ alive: globalThis.__jarvisRec === state, events: queue.splice(0) });
        };
        const timer = setTimeout(fire, waitMs);
        waiter = fire;
      });
    },
    // off removes the listeners and returns the events not yet collected.
    off() {
      flushTyping();
      for (const [type, fn] of listeners) removeEventListener(type, fn, true);
      if (globalThis.__jarvisRec === state) delete globalThis.__jarvisRec;
      const events = queue.splice(0);
      wake();
      return events;
    },
  };
  globalThis.__jarvisRec = state;
  return { token: state.token };
}

// recPoll collects this frame's recorded events (recInstall's poll).
function recPoll(rid, token, waitMs) {
  const r = globalThis.__jarvisRec;
  if (!r || r.rid !== rid || r.token !== token) return { alive: false, events: [] };
  return r.poll(waitMs);
}

// recStop ends recording rid in this frame and returns the events it still held.
function recStop(rid) {
  const r = globalThis.__jarvisRec;
  return { events: r && r.rid === rid ? r.off() : [] };
}

// --- start ---

api.runtime.onStartup.addListener(connect);
api.runtime.onInstalled.addListener(connect);
// Wakes a stopped service worker / event page to reconnect (and retries a missing host).
api.alarms.create("jarvis-reconnect", { periodInMinutes: 1 });
api.alarms.onAlarm.addListener((a) => {
  if (a.name === "jarvis-reconnect") connect();
});
connect();

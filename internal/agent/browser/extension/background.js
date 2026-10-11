// Jarvis Browser Control: runs commands from the Jarvis desktop agent on this computer.
//
// The only input is the native-messaging port to the agent's host ("io.jarvis.agent.browser"),
// which the browser starts from a manifest the agent registered. There are no content scripts,
// no runtime.onMessage / onMessageExternal / onConnectExternal listeners and no
// externally_connectable, so neither web pages nor other extensions can send this extension
// anything. Commands are a closed set (COMMANDS); anything else is refused.
//
// Page content is read, never written: no command types, clicks or fills a page, and no
// command reads form fields (inputs, textareas, selects, contenteditable regions, passwords).
// Clicking/typing is planned behind a confirmation step (docs/agent/DESIGN.md, browser section).
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
    scheduleRetry();
    return;
  }
  port = p;
  connectedAt = Date.now();
  p.onMessage.addListener((msg) => onMessage(p, msg));
  p.onDisconnect.addListener(() => {
    void api.runtime.lastError; // "host not found" / host exited: read so Chrome doesn't warn
    if (port === p) port = null;
    // A port that lived a while was a working host that went away: retry quickly.
    if (Date.now() - connectedAt > 10000) retryMs = RETRY_MIN_MS;
    scheduleRetry();
  });
  hello(p);
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
  });
}

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
};

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

// --- start ---

api.runtime.onStartup.addListener(connect);
api.runtime.onInstalled.addListener(connect);
// Wakes a stopped service worker / event page to reconnect (and retries a missing host).
api.alarms.create("jarvis-reconnect", { periodInMinutes: 1 });
api.alarms.onAlarm.addListener((a) => {
  if (a.name === "jarvis-reconnect") connect();
});
connect();

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

  async fill(args) {
    const t = await tabFor(args);
    const text = String(args.text ?? "");
    if (!text.trim()) throw fail("invalid_argument", "text is required");
    if (text.length > FILL_MAX) throw fail("invalid_argument", `text is longer than ${FILL_MAX} characters`);
    const field = clip(String(args.field ?? "").trim(), 100);
    const frame = await uniqueFrame(t, fieldAct, [field, "", false, false], "field");
    const [r] = await inject(t, { func: fieldAct, args: [field, text, !!args.submit, true], frameIds: [frame] });
    const res = r?.result;
    if (!res) throw fail("failed", "the page returned nothing");
    if (res.error) throw fail(res.code ?? "failed", res.error);
    return { tab: describe(t), ...res };
  },

  async click(args) {
    const t = await tabFor(args);
    const label = clip(String(args.text ?? "").trim(), 100);
    if (!label) throw fail("invalid_argument", "text (the button's label) is required");
    const frame = await uniqueFrame(t, buttonAct, [label, false], "button");
    const [r] = await inject(t, { func: buttonAct, args: [label, true], frameIds: [frame] });
    const res = r?.result;
    if (!res) throw fail("failed", "the page returned nothing");
    if (res.error) throw fail(res.code ?? "failed", res.error);
    return { tab: describe(t), ...res };
  },
};

const FILL_MAX = 2000;

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

// --- start ---

api.runtime.onStartup.addListener(connect);
api.runtime.onInstalled.addListener(connect);
// Wakes a stopped service worker / event page to reconnect (and retries a missing host).
api.alarms.create("jarvis-reconnect", { periodInMinutes: 1 });
api.alarms.onAlarm.addListener((a) => {
  if (a.name === "jarvis-reconnect") connect();
});
connect();

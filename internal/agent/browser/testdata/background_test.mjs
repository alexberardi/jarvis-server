// background.js against a fake browser API, through its only input: the native port.
// Run by TestBackgroundJS (extension_js_test.go): node --test thisfile.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const SRC = readFileSync(new URL("../extension/background.js", import.meta.url), "utf8");

const listener = () => {
  const fns = [];
  return { fns, addListener: (f) => fns.push(f) };
};

// load runs background.js with a fake api: tabs are the open tabs, granted whether it has
// access to websites, and blocked the urls whose injection the browser refuses.
function load({ tabs = [], granted = true, blocked = [], firefox = true, timers = false } = {}) {
  const sent = [];
  const injected = []; // tab ids recInstall ran in
  const port = { onMessage: listener(), onDisconnect: listener(), postMessage: (m) => sent.push(m) };
  const api = {
    runtime: {
      connectNative: () => port,
      onStartup: listener(),
      onInstalled: listener(),
      getManifest: () => ({ version: "0.0.0-test" }),
      ...(firefox ? { getBrowserInfo: async () => ({ name: "Firefox", version: "155" }) } : {}),
    },
    windows: { onFocusChanged: listener(), WINDOW_ID_NONE: -1, getLastFocused: async () => ({ focused: true }) },
    tabs: { query: async () => tabs, onUpdated: listener(), onRemoved: listener() },
    downloads: { onCreated: listener() },
    alarms: { create() {}, onAlarm: listener() },
    action: { setPopup: async () => {}, setBadgeText: async () => {}, setTitle: async () => {} },
    permissions: { contains: async (p) => granted && p.origins?.length === 1 && p.origins[0] === "<all_urls>" },
    scripting: {
      executeScript: async ({ target, func }) => {
        const t = tabs.find((x) => x.id === target.tabId);
        if (func.name === "recInstall") {
          if (blocked.includes(t?.url)) throw new Error("Missing host permission for the tab");
          injected.push(target.tabId);
          return [{ frameId: 0, result: { token: "tok" + target.tabId } }];
        }
        return [{ frameId: 0, result: { events: [], alive: false } }]; // recPoll: the document is gone
      },
    },
  };
  // timers: waits run at once (the page-load waits count tries, not the clock).
  const ctx = vm.createContext({ chrome: api, setTimeout: timers ? (f) => setImmediate(f) : () => 0, clearTimeout() {}, console });
  vm.runInContext(SRC, ctx);
  let next = 0;
  const call = async (cmd, args) => {
    const id = ++next;
    port.onMessage.fns.forEach((f) => f({ type: "command", id, cmd, args }));
    for (let i = 0; i < 1000; i++) {
      const r = sent.find((m) => m.type === "result" && m.id === id);
      if (r) return r;
      await new Promise((ok) => setImmediate(ok));
    }
    throw new Error(`no result for ${cmd}`);
  };
  return { call, sent, injected, ctx, api, events: () => sent.filter((m) => m.event === "record") };
}

const TABS = [
  { id: 1, url: "http://localhost:7710/chat" },
  { id: 2, url: "https://bank.example.com/" },
  { id: 3, url: "about:addons" },
];

test("record_start without access to websites fails with a clear error and records nothing", async () => {
  const b = load({ tabs: TABS, granted: false });
  const r = await b.call("record_start", { recording_id: "rec_1" });
  assert.equal(r.ok, false);
  assert.equal(r.code, "no_site_access");
  assert.match(r.error, /about:addons/);
  assert.match(r.error, /Access your data for all websites/);
  assert.deepEqual(b.injected, []);
  // Nothing is left running: once access is given, a new recording starts.
  const again = await b.call("record_stop", { recording_id: "rec_1" });
  assert.equal(again.code, "not_recording");
});

test("the error says where Chromium keeps site access", async () => {
  const b = load({ tabs: TABS, granted: false, firefox: false });
  const r = await b.call("record_start", { recording_id: "rec_1" });
  assert.equal(r.code, "no_site_access");
  assert.match(r.error, /chrome:\/\/extensions/);
});

test("record_start watches every open web page", async () => {
  const b = load({ tabs: TABS });
  const r = await b.call("record_start", { recording_id: "rec_1" });
  assert.equal(r.ok, true);
  assert.equal(r.result.tabs, 2);
  assert.deepEqual(b.injected.sort(), [1, 2]);
  assert.deepEqual(b.events(), []);
});

test("a page that can't be watched is reported, not skipped silently", async () => {
  const b = load({ tabs: TABS, blocked: ["https://bank.example.com/"] });
  const r = await b.call("record_start", { recording_id: "rec_1" });
  assert.equal(r.ok, true);
  assert.equal(r.result.tabs, 1);
  const evs = b.events();
  assert.equal(evs.length, 1);
  assert.equal(evs[0].kind, "cannot_watch");
  assert.equal(evs[0].url, "https://bank.example.com/");
  assert.equal(evs[0].tab, 2);
  assert.equal(evs[0].recording_id, "rec_1");
});

// fakeInput is an <input type=...> as actDo sees it, in ctx's document.
function fakeInput(ctx, type) {
  const doc = { execCommand: () => false };
  ctx.document = doc;
  ctx.Event = class { constructor(type) { this.type = type; } };
  ctx.HTMLInputElement = class { set value(v) { this._value = v; } get value() { return this._value ?? ""; } };
  const el = Object.assign(Object.create(ctx.HTMLInputElement.prototype), {
    localName: "input", isConnected: true, ownerDocument: doc, disabled: false, readOnly: false, events: [],
    getAttribute: (k) => (k === "type" ? type : null),
    scrollIntoView() {}, focus() {}, select() {},
    dispatchEvent(e) { this.events.push(e.type); },
  });
  ctx.__jarvisSnap = { gen: 1, refs: new Map([[1, el]]) };
  return el;
}

test("set_value fills a date field (the native setter, with input and change events)", () => {
  const { ctx } = load();
  for (const type of ["date", "time", "datetime-local", "month", "week"]) {
    const el = fakeInput(ctx, type);
    const r = ctx.actDo(1, 1, "set_value", "2026-11-01", []);
    assert.equal(r?.error, undefined, type);
    assert.equal(el.value, "2026-11-01", type);
    assert.deepEqual(el.events, ["input", "change"], type);
  }
  fakeInput(ctx, "checkbox");
  assert.equal(ctx.actDo(1, 1, "set_value", "x", []).code, "invalid_argument");
});

// #420: "go to google.com and search for X" filled 1.4 s after open, while the new tab was still
// loading. fill waits for the tab to load and for the field to appear, and says field_not_found
// (never a success) when it doesn't.
function loadingPage(b, { loadingFor, fieldAfter }) {
  const tab = { id: 9, url: "", pendingUrl: "https://www.google.com/", status: "loading", active: true };
  let gets = 0;
  let probes = 0;
  const filled = [];
  b.api.tabs.query = async () => [tab];
  b.api.tabs.get = async () => {
    if (++gets > loadingFor) Object.assign(tab, { status: "complete", url: "https://www.google.com/", pendingUrl: undefined });
    return { ...tab };
  };
  b.api.scripting.executeScript = async ({ target, args }) => {
    if (tab.status === "loading") throw new Error("Cannot access contents of url \"about:blank\"");
    if (target.allFrames) {
      const there = ++probes > fieldAfter;
      return [{ frameId: 0, result: { tier: there ? 3 : 0, count: there ? 1 : 0, labels: ["Search"], available: [] } }];
    }
    filled.push(args[1]);
    return [{ frameId: 0, result: { filled: "Search", submitted: args[2] } }];
  };
  return filled;
}

test("fill waits for a loading tab and a field the page adds late", async () => {
  const b = load({ timers: true });
  const filled = loadingPage(b, { loadingFor: 5, fieldAfter: 2 });
  const r = await b.call("fill", { field: "search", text: "best dog grooming", submit: true });
  assert.equal(r.ok, true, r.error);
  assert.equal(r.result.submitted, true);
  assert.equal(r.result.tab.url, "https://www.google.com/");
  assert.deepEqual(filled, ["best dog grooming"]);
});

test("fill on a page that never shows the field fails with field_not_found", async () => {
  const b = load({ timers: true });
  const filled = loadingPage(b, { loadingFor: 1, fieldAfter: Infinity });
  const r = await b.call("fill", { field: "search", text: "x" });
  assert.equal(r.ok, false);
  assert.equal(r.code, "field_not_found");
  assert.deepEqual(filled, []);
});

// Voice selection reads an invisible snapshot and acts by the refs it returns.
test("snapshot lists visible controls without drawing an overlay", async () => {
  const tab = { id: 4, url: "https://www.google.com/search?q=dogs", title: "dogs", active: true, windowId: 1 };
  const b = load({ tabs: [tab] });
  const calls = [];
  b.api.tabs.get = async () => ({ ...tab, status: "complete" });
  b.api.scripting.executeScript = async ({ func, args }) => {
    calls.push([func.name, args]);
    if (func.name === "snapCollect") {
      return [{ frameId: 0, result: { token: "t", url: tab.url, title: tab.title, elements: [{ k: 1, role: "link", name: "Wikipedia" }, { k: 2, role: "button", name: "Search" }] } }];
    }
    return [{ frameId: 0, result: { ok: true, count: 2 } }];
  };
  const r = await b.call("snapshot", { viewport: true });
  assert.equal(r.ok, true, JSON.stringify(r));
  assert.deepEqual(Array.from(r.result.elements, (e) => e.ref), ["e1", "e2"]);
  assert.equal(calls.find(([n]) => n === "snapCollect")[1][2], true, "only elements on screen");
  assert.equal(calls.some(([n]) => n === "hintsDraw"), false, "snapshot stays invisible");
  assert.ok(calls.some(([n]) => n === "snapCommit"), "snapshot refs are retained for act");
});

// fakePage is the document snapCollect reads: Bob's comment (a list item with a Reply link), a
// link far below the fold, and a draft in a text box. top is the frame's top window.
function fakePage(ctx, { subframe = false } = {}) {
  const node = (props) => {
    const e = { localName: "a", attrs: {}, parentElement: null, labels: null, isContentEditable: false,
      innerText: "", textContent: "", rect: { left: 10, top: 10, width: 100, height: 20 }, ...props };
    e.getAttribute = (k) => e.attrs[k] ?? null;
    e.hasAttribute = (k) => k in e.attrs;
    e.removeAttribute = () => {};
    e.closest = () => null;
    e.matches = () => e.container === true;
    e.querySelector = () => null;
    e.getClientRects = () => [e.rect];
    e.getBoundingClientRect = () => ({ ...e.rect, right: e.rect.left + e.rect.width, bottom: e.rect.top + e.rect.height });
    e.checkVisibility = () => true;
    return e;
  };
  const comment = node({ localName: "li", container: true, texts: ["Bob", "· 2h ·", "Great point"] });
  const reply = node({ attrs: { href: "/c/1" }, href: "https://forum.test/c/1", innerText: "Reply", parentElement: comment });
  const far = node({ attrs: { href: "/old" }, href: "https://forum.test/old", innerText: "Older posts", rect: { left: 10, top: 5000, width: 100, height: 20 } });
  const draft = node({ localName: "input", attrs: { type: "text" }, value: "my draft" });
  ctx.innerWidth = 1200;
  ctx.innerHeight = 800;
  ctx.location = { href: "https://forum.test/" };
  ctx.NodeFilter = { SHOW_TEXT: 4 };
  ctx.document = {
    title: "Forum",
    getElementById: () => null,
    querySelectorAll: (sel) => (sel === "[data-jarvis-ref]" ? [] : [reply, far, draft]),
    createTreeWalker: (root) => {
      const nodes = (root.texts ?? []).map((t) => ({ nodeValue: t, parentElement: node({ localName: "span" }) }));
      let i = 0;
      return { nextNode: () => nodes[i++] ?? null };
    },
  };
  ctx.window = ctx;
  ctx.top = subframe ? {} : ctx;
}

test("both snapshot kinds carry context, position and target; no position offscreen or in a subframe", () => {
  const { ctx } = load();
  fakePage(ctx);
  const task = ctx.snapCollect(1, 300, false).elements; // a computer task's snapshot: the whole page
  assert.deepEqual(Array.from(task, (e) => e.name), ["Reply", "Older posts", ""]);
  assert.equal(task[0].context, "Bob · 2h · Great point");
  assert.equal(task[0].position, "top-left");
  assert.equal(task[0].target, "https://forum.test/c/1");
  assert.equal(task[1].position, undefined, "a link below the fold has no position");
  assert.equal(task[1].target, "https://forum.test/old");
  assert.equal(task[2].value, "my draft", "computer snapshots keep field values");
  assert.equal(task[2].context, undefined);

  const voice = ctx.snapCollect(2, 150, true).elements; // a voice snapshot: what's on screen
  assert.deepEqual(Array.from(voice, (e) => e.name), ["Reply", ""]);
  assert.equal(voice[0].context, "Bob · 2h · Great point");
  assert.equal(voice[1].value, undefined, "voice snapshots never carry a field's value");

  fakePage(ctx, { subframe: true });
  const framed = ctx.snapCollect(3, 300, false).elements;
  assert.equal(framed[0].position, undefined, "positions inside a subframe aren't mapped to the page yet");
  assert.equal(framed[0].context, "Bob · 2h · Great point");
  assert.equal(framed[0].target, "https://forum.test/c/1");
});

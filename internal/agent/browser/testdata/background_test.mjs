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
function load({ tabs = [], granted = true, blocked = [], firefox = true } = {}) {
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
  const ctx = vm.createContext({ chrome: api, setTimeout: () => 0, clearTimeout() {}, console });
  vm.runInContext(SRC, ctx);
  let next = 0;
  const call = async (cmd, args) => {
    const id = ++next;
    port.onMessage.fns.forEach((f) => f({ type: "command", id, cmd, args }));
    for (let i = 0; i < 100; i++) {
      const r = sent.find((m) => m.type === "result" && m.id === id);
      if (r) return r;
      await new Promise((ok) => setImmediate(ok));
    }
    throw new Error(`no result for ${cmd}`);
  };
  return { call, sent, injected, ctx, events: () => sent.filter((m) => m.event === "record") };
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

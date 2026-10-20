// A fake WebExtension API and a loader that runs the extension's scripts in it (node:vm), for the
// extension's unit tests (`node --test internal/agent/browser/jstest/`). Only what the scripts
// touch at load time and in the tested paths is real; every other event is an inert listener list.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const EXT = fileURLToPath(new URL("../extension/", import.meta.url));

// event is a fake browser event: addListener records, fire calls every listener.
export function event() {
  const listeners = [];
  return {
    listeners,
    addListener: (fn) => listeners.push(fn),
    removeListener: (fn) => listeners.splice(listeners.indexOf(fn) >>> 0, 1),
    fire: (...args) => listeners.map((fn) => fn(...args)),
  };
}

// namespace is an API namespace whose on* members are events and whose other members are
// what fields gives (missing functions resolve to undefined).
function namespace(fields = {}) {
  return new Proxy(fields, {
    get(t, k) {
      if (!(k in t)) t[k] = typeof k === "string" && k.startsWith("on") ? event() : async () => undefined;
      return t[k];
    },
  });
}

// fakeAPI is a browser with website access granted or not. permissions.request grants it
// (answering yes in the prompt) unless deny is set.
export function fakeAPI({ access = true, deny = false, version = "9.9.9" } = {}) {
  const state = { access, requests: [], posted: [], popups: [] };
  const port = { onMessage: event(), onDisconnect: event(), postMessage: (m) => state.posted.push(JSON.parse(JSON.stringify(m))) };
  const permissions = namespace({
    contains: async (p) => {
      if (state.throwContains) throw new Error("permissions unavailable");
      return JSON.stringify(p) === JSON.stringify({ origins: ["<all_urls>"] }) && state.access;
    },
    request: (p) => {
      state.requests.push(JSON.parse(JSON.stringify(p)));
      if (!deny) state.access = true;
      return Promise.resolve(!deny);
    },
  });
  const api = {
    runtime: namespace({
      connectNative: () => port,
      getManifest: () => ({ version }),
      getBrowserInfo: async () => ({ name: "Firefox", version: "140.0" }),
    }),
    windows: namespace({ WINDOW_ID_NONE: -1, getLastFocused: async () => ({ focused: true }) }),
    tabs: namespace(),
    downloads: namespace(),
    alarms: namespace({ create: () => {} }),
    action: namespace({
      setPopup: async (o) => state.popups.push(o.popup),
      setBadgeText: async () => {},
      setTitle: async () => {},
    }),
    scripting: namespace(),
    permissions,
  };
  return { api, state, port };
}

// settle lets pending promise chains run.
export const settle = () => new Promise((r) => setImmediate(r));

// loadBackground runs background.js against api, with timers that never fire.
export function loadBackground(api) {
  const ctx = vm.createContext({
    browser: api,
    console,
    URL,
    setTimeout: () => 0,
    clearTimeout: () => {},
    setInterval: () => 0,
    clearInterval: () => {},
  });
  vm.runInContext(readFileSync(EXT + "background.js", "utf8"), ctx, { filename: "background.js" });
  return ctx;
}

// element is a fake DOM element: what popup.js sets and the click it listens for.
function element(id) {
  const classes = new Set();
  const on = {};
  return {
    id,
    textContent: "",
    hidden: false,
    href: "",
    disabled: false,
    classList: { toggle: (c, v) => (v ? classes.add(c) : classes.delete(c)), contains: (c) => classes.has(c) },
    addEventListener: (type, fn) => (on[type] = fn),
    click() {
      on.click?.({ preventDefault() {} });
    },
  };
}

// loadPopup runs popup.js against api with popup.html's elements, opened as popup.html?state=state.
export function loadPopup(api, state = "connected") {
  const ids = [...readFileSync(EXT + "popup.html", "utf8").matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]);
  const els = Object.fromEntries(ids.map((id) => [id, element(id)]));
  const win = { closed: false };
  const ctx = vm.createContext({
    browser: api,
    console,
    URLSearchParams,
    location: { search: "?state=" + state },
    document: { getElementById: (id) => els[id] ?? null },
    window: { close: () => (win.closed = true) },
  });
  vm.runInContext(readFileSync(EXT + "popup.js", "utf8"), ctx, { filename: "popup.js" });
  return { els, win };
}

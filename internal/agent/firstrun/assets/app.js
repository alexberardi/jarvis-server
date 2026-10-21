// The setup page of Jarvis Automation Agent. It talks only to the agent that served it, with
// relative URLs (the page's own path carries the per-launch secret). Text from the server goes
// into the page as text, never as HTML.
"use strict";

const $ = (id) => document.getElementById(id);
const show = (el, on) => { (typeof el === "string" ? $(el) : el).hidden = !on; };

let state = null;
let finished = false;
let busy = false;
let micUntil = 0;

async function api(path, body) {
  const opts = body === undefined
    ? { method: "GET", cache: "no-store" }
    : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  let resp;
  try {
    resp = await fetch("api/" + path, opts);
  } catch (e) {
    throw new Error("The setup page lost touch with Jarvis Automation Agent. If you closed it, open it again from its menu.");
  }
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(data.error || "Something went wrong (" + resp.status + ").");
  return data;
}

function setError(id, msg) {
  const el = $(id);
  el.textContent = msg || "";
  show(el, !!msg);
}

// --- browser detection (for the store button) ---

function browserFamily() {
  const ua = navigator.userAgent;
  if (/Firefox\//.test(ua)) return "firefox";
  if (/(Chrome|Chromium|Edg)\//.test(ua)) return "chrome";
  return "";
}

function storeLink(kind, stores, primary) {
  const s = stores[kind];
  const a = document.createElement("a");
  a.className = "store" + (primary ? "" : " secondary");
  a.target = "_blank";
  a.rel = "noopener noreferrer";
  const name = kind === "firefox" ? "Firefox" : "Chrome, Edge or Brave";
  if (s.published) {
    a.href = s.url;
    a.textContent = "Add to " + name;
  } else {
    a.href = stores.help;
    a.textContent = "Add to " + name + " (coming soon)";
  }
  return a;
}

function renderBrowsers(stores) {
  const fam = browserFamily();
  const main = $("browser-buttons"), others = $("browser-others");
  main.replaceChildren();
  others.replaceChildren();
  for (const kind of ["firefox", "chrome"]) {
    if (kind === fam) main.append(storeLink(kind, stores, true));
    else others.append(storeLink(kind, stores, false));
  }
  if (!fam) {
    main.append(document.createTextNode("Jarvis can use Firefox, Chrome, Edge, Brave, Arc or Vivaldi. Open this page in one of them, or pick one below."));
  }
  const unpublished = !stores.firefox.published || !stores.chrome.published;
  $("browser-note").textContent = unpublished
    ? "The extension isn't in the browser stores yet. The button opens help for now."
    : "Your browser asks before adding it.";
  show("browser-note", true);
}

// --- rendering ---

function renderServer(s) {
  const picked = !!s.server;
  show("server-chosen", picked);
  show("server-pick", !picked && !s.joined);
  if (picked) {
    const where = s.server.where === "this computer" ? " (on this computer)" : "";
    $("server-name").textContent = "Using " + (s.server.name || s.server.url) + where;
  }
  show("server-change", picked && !s.joined && s.join.state !== "waiting" && s.join.state !== "confirm_household");
  $("server-step").classList.toggle("done", picked);
}

function renderJoin(s) {
  const j = s.join;
  show("join-step", !!s.server || s.joined);
  const joined = s.joined || j.state === "joined";
  $("join-step").classList.toggle("done", joined);
  show("join-joined", joined);
  setText("join-warning", j.warning);
  const signinOpen = !$("signin-form").hidden;
  const confirming = j.state === "confirm_household";
  show("join-choose", !joined && j.state !== "waiting" && j.state !== "choose_household" && !confirming && !signinOpen);
  show("join-code", !joined && j.state === "waiting");
  show("join-confirm", !joined && confirming);
  if (confirming) {
    $("confirm-household").textContent = j.household;
    const by = [j.approved_by ? "Approved by " + j.approved_by : "", j.room ? "room: " + j.room : ""].filter(Boolean);
    setText("confirm-by", by.join(", "));
  }
  if (j.state === "waiting") {
    $("code").textContent = j.code;
    $("code-name").textContent = s.computer;
    const m = Math.floor(j.expires_in / 60), sec = String(j.expires_in % 60).padStart(2, "0");
    $("code-left").textContent = "(the code works for " + m + ":" + sec + ")";
  }
  if (joined || j.state === "waiting" || j.state === "choose_household" || confirming) show("signin-form", false);
  show("household-form", !joined && j.state === "choose_household");
  if (j.state === "choose_household" && $("household-list").childElementCount === 0) {
    const list = $("household-list");
    j.households.forEach((h, i) => {
      const li = document.createElement("li");
      const label = document.createElement("label");
      const r = document.createElement("input");
      r.type = "radio"; r.name = "household"; r.value = h.id; r.checked = i === 0;
      label.append(r, document.createTextNode(" " + h.name));
      li.append(label);
      list.append(li);
    });
  }
  if (j.state !== "choose_household") $("household-list").replaceChildren();
  setError("join-error", j.state === "failed" ? j.error : "");
  return joined;
}

function setText(id, text) {
  $(id).textContent = text || "";
  show(id, !!text);
}

function renderPermissions(perms, os) {
  show("perm-step", perms && perms.length > 0);
  if (!perms || !perms.length) return;
  const mac = os === "darwin";
  $("perm-title").textContent = mac ? "Let Jarvis Automation Agent work on this Mac" : "Let Jarvis Automation Agent watch the keyboard and mouse";
  $("perm-intro").textContent = mac ? "macOS asks you to allow each of these once. The ticks turn green as you do."
    : "Only for recording what you do on the desktop. Run the command once in a terminal, then log out and back in.";
  const list = $("perm-list");
  list.replaceChildren();
  let all = true;
  for (const p of perms) {
    const ok = p.status === "granted";
    all = all && ok;
    const li = document.createElement("li");
    const dot = document.createElement("span");
    dot.className = "dot" + (ok ? " ok" : "");
    dot.textContent = ok ? "✓" : "";
    dot.setAttribute("aria-label", ok ? "allowed" : "not allowed yet");
    const label = document.createElement("strong");
    label.textContent = p.label;
    const action = document.createElement("span");
    if (!ok && p.how) {
      const code = document.createElement("code");
      code.textContent = p.how;
      action.append(code);
    } else if (!ok) {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = p.status === "denied" ? "Open Settings" : "Allow";
      b.addEventListener("click", () => act(() => api("permission", { id: p.id }), "fatal"));
      action.append(b);
    } else {
      action.className = "muted";
      // macOS gives the running agent this grant only when it starts again.
      action.textContent = p.restart ? "Granted — restart Jarvis to apply" : "Allowed";
    }
    const why = document.createElement("span");
    why.className = "why";
    why.textContent = p.why;
    li.append(dot, label, action, why);
    list.append(li);
  }
  $("perm-step").classList.toggle("done", all);
}

function renderPortal(p) {
  show("portal-step", !!p);
  if (!p) return;
  show("portal-ok", p.allowed);
  show("portal-ask", !p.allowed && p.available);
  setText("portal-why", !p.allowed && !p.available ? "This desktop can't allow typing right now: " + p.why : "");
  $("portal-step").classList.toggle("done", p.allowed);
}

function renderVoice(s) {
  show("voice-step", !!s.voice || s.mic);
  if (s.voice) {
    show($("voice-on").closest("label"), true);
    $("voice-on").checked = s.voice.on;
  } else {
    show($("voice-on").closest("label"), false);
  }
  show("mic-block", s.mic);
}

// Opened again once set up (the app reopened while Jarvis runs): say it is running, with links
// to its settings on this page.
function renderRunning(s, joined) {
  const running = joined && s.running;
  $("title").textContent = running ? "Jarvis Automation" : "Set up Jarvis Automation";
  show("running", running);
  show("running-perms", !$("perm-step").hidden);
  show("running-voice", !$("voice-step").hidden);
}

function numberSteps() {
  let n = 0;
  for (const sec of document.querySelectorAll("#steps > section.card")) {
    if (!sec.hidden) sec.querySelector(".num").textContent = String(++n);
  }
}

function render(s) {
  state = s;
  if (finished) return;
  show("steps", true);
  $("computer").textContent = "on " + s.computer;
  renderServer(s);
  const joined = renderJoin(s);
  show("browser-step", joined);
  if (joined) renderBrowsers(s.stores);
  renderPermissions(s.permissions, s.os);
  renderPortal(s.portal);
  renderVoice(s);
  renderRunning(s, joined);
  show("done-row", joined);
  show("remove-open", s.can_remove && $("remove-confirm").hidden);
  numberSteps();
}

// --- servers ---

async function refreshServers() {
  try {
    const r = await api("servers");
    const list = $("server-list");
    list.replaceChildren();
    for (const s of r.servers) {
      const li = document.createElement("li");
      const b = document.createElement("label");
      const radio = document.createElement("input");
      radio.type = "radio";
      radio.name = "server";
      radio.addEventListener("change", () => pickServer(s.url));
      const where = document.createElement("span");
      where.className = "where";
      where.textContent = s.where === "this computer" ? "on this computer" : "on your network";
      b.append(radio, document.createTextNode(" " + (s.name || s.url)), where);
      li.append(b);
      list.append(li);
    }
    show("server-searching", r.searching);
    show("server-none", !r.searching && r.servers.length === 0);
    if (r.searching) setTimeout(refreshServers, 800);
  } catch (e) {
    setError("server-error", e.message);
  }
}

async function pickServer(url) {
  setError("server-error", "");
  await act(async () => render(await api("server", { url })), "server-error");
}

// --- actions ---

async function act(fn, errId) {
  if (busy) return;
  busy = true;
  try {
    await fn();
  } catch (e) {
    setError(errId, e.message);
  } finally {
    busy = false;
  }
}

async function poll() {
  if (finished) return;
  try {
    render(await api("state"));
    setError("fatal", "");
  } catch (e) {
    setError("fatal", e.message);
  }
  setTimeout(poll, document.hidden ? 10000 : 2000);
}

async function micLoop() {
  if (finished || Date.now() > micUntil) {
    $("mic-bar").style.width = "0";
    $("mic-test").textContent = "Test my microphone";
    micUntil = 0;
    return;
  }
  try {
    const r = await api("mic");
    setError("mic-error", r.error);
    $("mic-bar").style.width = Math.round(r.level * 100) + "%";
    if (r.error) micUntil = 0;
  } catch (e) {
    setError("mic-error", e.message);
    micUntil = 0;
  }
  setTimeout(micLoop, 120);
}

function finish(title, text) {
  finished = true;
  show("steps", false);
  $("finished-title").textContent = title;
  $("finished-text").textContent = text;
  show("finished", true);
}

function wire() {
  $("server-search").addEventListener("click", () => act(async () => {
    await api("servers/search", {});
    show("server-searching", true);
    refreshServers();
  }, "server-error"));
  $("server-form").addEventListener("submit", (e) => {
    e.preventDefault();
    pickServer($("server-address").value);
  });
  $("server-change").addEventListener("click", () => act(async () => {
    render(await api("server/forget", {}));
    await api("servers/search", {});
    refreshServers();
  }, "server-error"));
  $("join-pair").addEventListener("click", () => act(async () => render(await api("pair", {})), "join-error"));
  $("code-cancel").addEventListener("click", () => act(async () => render(await api("pair/cancel", {})), "join-error"));
  $("confirm-yes").addEventListener("click", () => act(async () => render(await api("pair/confirm", {})), "join-error"));
  $("confirm-no").addEventListener("click", () => act(async () => render(await api("pair/cancel", {})), "join-error"));
  $("join-signin").addEventListener("click", () => {
    setError("join-error", "");
    show("join-choose", false);
    show("signin-form", true);
    $("email").focus();
  });
  $("signin-back").addEventListener("click", () => {
    show("signin-form", false);
    show("join-choose", true);
  });
  $("signin-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const pw = $("password");
    act(async () => {
      try {
        render(await api("signin", { email: $("email").value, password: pw.value }));
      } finally {
        pw.value = "";
      }
    }, "join-error");
  });
  $("household-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const r = document.querySelector('input[name="household"]:checked');
    if (!r) return;
    act(async () => render(await api("signin/household", { household_id: r.value })), "join-error");
  });
  $("voice-on").addEventListener("change", (e) => act(async () => render(await api("voice", { on: e.target.checked })), "fatal"));
  $("mic-test").addEventListener("click", () => {
    if (micUntil) { micUntil = 0; return; }
    micUntil = Date.now() + 30000;
    $("mic-test").textContent = "Stop";
    micLoop();
  });
  $("portal-allow").addEventListener("click", () => act(async () => {
    show("portal-wait", true);
    try {
      render(await api("portal", {}));
    } finally {
      show("portal-wait", false);
    }
  }, "portal-why"));
  $("done").addEventListener("click", () => act(async () => {
    await api("done", {});
    const listening = state && state.voice && state.voice.on;
    finish("You're all set", "Jarvis is running on this computer." + (listening ? " Say \"Hey Jarvis\" to start." : "") +
      " You can close this tab. To come back here, choose Set up / Settings… in the Jarvis Automation menu (the shield icon).");
  }, "fatal"));
  $("remove-open").addEventListener("click", () => { show("remove-confirm", true); show("remove-open", false); });
  $("remove-no").addEventListener("click", () => { show("remove-confirm", false); show("remove-open", true); });
  $("remove-yes").addEventListener("click", () => act(async () => {
    $("remove-yes").disabled = true;
    try {
      const r = await api("remove", { confirm: "remove" });
      finish("Removed", "Jarvis Automation Agent was removed from this computer. " + (r.note || ""));
    } finally {
      $("remove-yes").disabled = false;
    }
  }, "remove-error"));
}

async function start() {
  wire();
  try {
    render(await api("state"));
  } catch (e) {
    setError("fatal", e.message);
  }
  if (state && !state.joined && !state.server) {
    api("servers/search", {}).then(refreshServers).catch((e) => setError("server-error", e.message));
  }
  setTimeout(poll, 2000);
}

start();

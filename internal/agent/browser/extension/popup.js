// The toolbar popup: whether the desktop agent's native host answered, and a link to download the
// agent when it didn't; whether the extension has website access, and a button to turn it on. background.js puts the state in this page's URL (action.setPopup); the
// page sends the background nothing. The link is public (the release page): the extension never
// learns the user's own Jarvis server address.
"use strict";

const api = globalThis.browser ?? globalThis.chrome;
const DOWNLOAD_URL = "https://github.com/alexberardi/jarvis-server/releases";

const TEXT = {
  connected: {
    title: "Desktop agent set up",
    message: "The Jarvis Automation desktop agent is set up on this computer. Jarvis can use this browser while the agent is running.",
    hint: "",
    download: false,
  },
  missing: {
    title: "Desktop agent not installed",
    message: "Jarvis controls this browser through the Jarvis Automation desktop agent, which isn't installed on this computer.",
    hint: "Install it, sign in, and this browser connects by itself.",
    download: true,
  },
  unreachable: {
    title: "Desktop agent not connected",
    message: "The Jarvis Automation desktop agent on this computer didn't answer.",
    hint: "Start the agent, or update it if it's an older version. If it isn't installed yet, download it.",
    download: true,
  },
};

const state = new URLSearchParams(location.search).get("state");
const t = TEXT[state] ?? {
  title: "Desktop agent not connected",
  message: "Jarvis controls this browser through the Jarvis Automation desktop agent on this computer.",
  hint: "If it isn't installed yet, download it.",
  download: true,
};

document.getElementById("heading").classList.toggle("ok", state === "connected");
document.getElementById("title").textContent = t.title;
document.getElementById("message").textContent = t.message;
const hint = document.getElementById("hint");
hint.textContent = t.hint;
hint.hidden = !t.hint;
const link = document.getElementById("download");
link.hidden = !t.download;
link.href = DOWNLOAD_URL;
link.addEventListener("click", (e) => {
  e.preventDefault();
  Promise.resolve(api.tabs.create({ url: DOWNLOAD_URL }))
    .catch(() => {})
    .finally(() => window.close());
});

// Website access (the <all_urls> host permission, which Firefox lets the user turn off): read
// here with permissions.contains, asked for with permissions.request. The background learns of a
// change from permissions.onAdded/onRemoved, so this page still sends it nothing.
const ALL_SITES = { origins: ["<all_urls>"] };
const access = document.getElementById("access");
const accessOn = document.getElementById("access-on");

function showAccess(on) {
  access.hidden = false;
  access.classList.toggle("off", !on);
  document.getElementById("access-state").textContent = on ? "on" : "off";
  document.getElementById("access-hint").hidden = on;
  accessOn.hidden = on;
}

function readAccess() {
  return Promise.resolve(api.permissions.contains(ALL_SITES))
    .then((on) => showAccess(!!on))
    .catch(() => {}); // the browser can't say: show nothing
}

accessOn.addEventListener("click", () => {
  // Called straight from the click, with nothing awaited first: Firefox shows its prompt only
  // inside the user's gesture.
  Promise.resolve(api.permissions.request(ALL_SITES))
    .catch(() => false)
    .then(readAccess);
});

readAccess();

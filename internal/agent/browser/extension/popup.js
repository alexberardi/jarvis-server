// The toolbar popup: whether the desktop agent's native host answered, and a link to download the
// agent when it didn't. background.js puts the state in this page's URL (action.setPopup); the
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

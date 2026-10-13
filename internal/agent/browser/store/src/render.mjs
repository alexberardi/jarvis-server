// Renders the store images for Jarvis Browser Control, and the extension's own icons, from
// the logo and the island's golden frames. Headless Chromium via Playwright; nothing opens a
// window. Same inputs, same Chromium, same fonts (Inter) -> same bytes.
//
//   cd "$(mktemp -d)" && npm i playwright@1 && npx playwright install chromium
//   NODE_PATH=$PWD/node_modules node <repo>/internal/agent/browser/store/src/render.mjs
//
// Writes store/images/*.png and extension/icon-{48,128}.png. Every page, tab, URL and person
// shown is made up (the demo household's Jordan; .example domains), so the images carry no
// one's real data.
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(join(process.env.NODE_PATH ?? process.cwd(), "noop.js"));
const { chromium } = require("playwright");

const SRC = dirname(fileURLToPath(import.meta.url));
const STORE = dirname(SRC);
const EXT = join(STORE, "..", "extension");
const ISLAND = join(STORE, "..", "..", "island", "testdata");
const OUT = join(STORE, "images");

const dataURI = (p) => "data:image/png;base64," + readFileSync(p).toString("base64");
const LOGO = dataURI(join(SRC, "logo-shield.png"));
const ISLAND_LISTENING = dataURI(join(ISLAND, "listening.png"));
const ISLAND_SPEAKING = dataURI(join(ISLAND, "speaking.png"));

const INDIGO = "#3a2d8c";
const INDIGO_DARK = "#211a5c";
const LAVENDER = "#e8e4f5";

const BASE_CSS = `
  * { box-sizing: border-box; margin: 0; padding: 0; }
  html, body { width: 100%; height: 100%; }
  body { font-family: Inter, "Liberation Sans", Arial, sans-serif; -webkit-font-smoothing: antialiased; }
  *, *::before, *::after { animation: none !important; transition: none !important; }
`;

// --- icons: the shield centred in a square, transparent around it ---

function iconPage(size, art) {
  return `<style>${BASE_CSS} body { background: transparent; display: grid; place-items: center; }
    img { height: ${art}px; width: auto; display: block; }</style><img src="${LOGO}">`;
}

// --- a browser window (generic: no vendor's chrome or logo) ---

function browserWindow({ width, height, tabs, url, page, overlay = "" }) {
  const tabHTML = tabs
    .map(
      (t) => `<div class="tab ${t.active ? "active" : ""}">
        <span class="fav" style="background:${t.color}"></span>
        <span class="tt">${t.title}</span>${t.audible ? '<span class="aud">&#9835;</span>' : ""}
        <span class="x">&times;</span></div>`,
    )
    .join("");
  return `<div class="win" style="width:${width}px;height:${height}px">
    <div class="strip"><div class="dots"><i></i><i></i><i></i></div>${tabHTML}<div class="plus">+</div></div>
    <div class="bar"><span class="nav">&#8592;</span><span class="nav">&#8594;</span><span class="nav">&#8635;</span>
      <div class="url"><span class="lock">&#128274;</span>${url}</div>
      <img class="ext" src="${LOGO}" title="Jarvis Browser Control"></div>
    <div class="page">${page}</div>${overlay}</div>`;
}

const WINDOW_CSS = `
  .win { position: relative; background: #fff; border-radius: 14px; overflow: hidden;
    box-shadow: 0 30px 80px rgba(10, 6, 40, .45), 0 0 0 1px rgba(255,255,255,.08); }
  .strip { height: 46px; background: #e9e7f0; display: flex; align-items: flex-end; padding: 0 10px; gap: 2px; }
  .dots { display: flex; gap: 8px; align-self: center; margin: 0 14px 0 6px; }
  .dots i { width: 12px; height: 12px; border-radius: 50%; background: #c9c5d8; display: block; }
  .tab { height: 36px; width: 230px; display: flex; align-items: center; gap: 8px; padding: 0 12px;
    border-radius: 10px 10px 0 0; font-size: 13px; color: #4a4560; }
  .tab.active { background: #fff; color: #1d1830; }
  .tab .fav { width: 14px; height: 14px; border-radius: 4px; flex: none; }
  .tab .tt { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .tab .aud { color: ${INDIGO}; font-size: 13px; }
  .tab .x { color: #8a85a0; font-size: 15px; }
  .plus { align-self: center; color: #6b6585; font-size: 20px; margin-left: 8px; }
  .bar { height: 48px; display: flex; align-items: center; gap: 14px; padding: 0 16px; border-bottom: 1px solid #ece9f3; }
  .nav { color: #6b6585; font-size: 18px; width: 18px; text-align: center; }
  .url { flex: 1; height: 32px; border-radius: 16px; background: #f3f1f8; display: flex; align-items: center;
    gap: 8px; padding: 0 16px; font-size: 14px; color: #3d3852; }
  .lock { font-size: 11px; filter: grayscale(1); opacity: .6; }
  .ext { height: 22px; }
  .page { position: absolute; inset: 94px 0 0 0; overflow: hidden; }
`;

// A made-up recipe site with a video, the scene for "pause the video" / "open the recipe".
const RECIPE_PAGE = `
  <div style="padding:34px 56px;display:grid;grid-template-columns:1.25fr 1fr;gap:40px;color:#211c33">
    <div>
      <div style="font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:#8b7fc8;font-weight:600">Breakfast &middot; 25 min</div>
      <div style="font-size:36px;font-weight:700;margin:10px 0 14px;line-height:1.15">Weekend pancakes</div>
      <div style="height:250px;border-radius:12px;background:linear-gradient(135deg,#f6c993,#e9926b 60%,#b4566a);position:relative">
        <div style="position:absolute;left:50%;top:50%;width:64px;height:64px;margin:-32px 0 0 -32px;border-radius:50%;background:rgba(0,0,0,.45);display:grid;place-items:center">
          <div style="width:0;height:0;border-left:16px solid #fff;border-top:10px solid transparent;border-bottom:10px solid transparent;margin-left:4px"></div></div>
        <div style="position:absolute;left:14px;right:14px;bottom:12px;height:4px;border-radius:2px;background:rgba(255,255,255,.4)">
          <div style="width:38%;height:100%;border-radius:2px;background:#fff"></div></div>
      </div>
    </div>
    <div style="padding-top:34px">
      <div style="font-size:18px;font-weight:600;margin-bottom:12px">Ingredients</div>
      ${["2 cups flour", "2 tsp baking powder", "1 &frac34; cups milk", "2 eggs"]
        .map((i) => `<div style="font-size:15px;padding:8px 0;border-bottom:1px solid #efedf5;color:#3d3852">${i}</div>`)
        .join("")}
    </div>
  </div>`;

// A made-up article, the scene for "read me this page".
const ARTICLE_PAGE = `
  <div style="max-width:700px;padding:38px 60px;color:#211c33">
    <div style="font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:#8b7fc8;font-weight:600">Garden notes</div>
    <div style="font-size:34px;font-weight:700;margin:10px 0 18px;line-height:1.2">Five vegetables you can still plant in autumn</div>
    ${[
      "Cooler weeks are a second spring for the kitchen garden. Garlic goes in now for a summer harvest, and broad beans sown before the first frost crop weeks earlier than spring sowings.",
      "Spinach and lettuce like the shorter days; a cloche keeps them picking into winter. Radishes are ready in a month, and onion sets need little more than loose soil and sun.",
      "Water in the morning, mulch the beds, and keep an eye on slugs after rain.",
    ]
      .map((p) => `<p style="font-size:17px;line-height:1.65;color:#3d3852;margin-bottom:16px">${p}</p>`)
      .join("")}
  </div>`;

function bubble(who, text, me) {
  return `<div style="display:flex;flex-direction:column;align-items:${me ? "flex-end" : "flex-start"};margin-top:12px">
    <div style="font-size:12px;font-weight:600;color:#8b84a6;margin:0 6px 4px">${who}</div>
    <div style="max-width:330px;padding:11px 15px;border-radius:16px;font-size:15px;line-height:1.45;
      ${me ? `background:${INDIGO};color:#fff;border-bottom-right-radius:5px` : `background:#f1eff8;color:#211c33;border-bottom-left-radius:5px`}">${text}</div></div>`;
}

function chatCard(lines, { right = 28, bottom = 26 } = {}) {
  return `<div style="position:absolute;right:${right}px;bottom:${bottom}px;width:390px;background:#fff;border-radius:18px;
      padding:14px 18px 18px;box-shadow:0 18px 50px rgba(30,20,90,.28),0 0 0 1px rgba(58,45,140,.08)">
    <div style="display:flex;align-items:center;gap:10px;padding-bottom:6px;border-bottom:1px solid #f0eef6">
      <img src="${LOGO}" style="height:24px"><div style="font-weight:600;font-size:14px;color:#211c33">Jarvis</div>
      <div style="margin-left:auto;font-size:12px;color:#8b84a6">voice</div></div>
    ${lines.join("")}</div>`;
}

function islandOverlay(src) {
  // The island is the agent's real on-screen voice indicator (internal/agent/island golden frames).
  // It sits at the top centre of the screen; here, just under the address bar.
  return `<img src="${src}" style="position:absolute;left:50%;top:100px;width:240px;margin-left:-120px">`;
}

function screenshotPage({ title, sub, window }) {
  return `<style>${BASE_CSS}${WINDOW_CSS}
    body { background: radial-gradient(1200px 700px at 20% 0%, #5241b8 0%, ${INDIGO} 45%, ${INDIGO_DARK} 100%);
      display: flex; flex-direction: column; align-items: center; padding-top: 44px; }
    h1 { color: #fff; font-size: 40px; font-weight: 700; letter-spacing: -.01em; text-align: center; }
    .sub { color: ${LAVENDER}; opacity: .85; font-size: 19px; margin: 10px 0 30px; text-align: center; }
  </style><h1>${title}</h1><div class="sub">${sub}</div>${window}`;
}

const TABS_VOICE = [
  { title: "Lo-fi beats to cook to", color: "#d64545", audible: true },
  { title: "Weekend pancakes", color: "#e9926b", active: true },
  { title: "Five vegetables for autumn", color: "#5aa469" },
];

const SHOTS = {
  "screenshot-1-voice-tabs": screenshotPage({
    title: "Ask Jarvis to open, read and close tabs — by voice.",
    sub: "Your own browser, your own logins, your own Jarvis.",
    window: browserWindow({
      width: 1120, height: 590, tabs: TABS_VOICE, url: "pancakes.example/weekend", page: RECIPE_PAGE,
      overlay: islandOverlay(ISLAND_SPEAKING) + chatCard([
        bubble("Jordan", "Hey Jarvis, stop the music and open the pancake recipe.", true),
        bubble("Jarvis", "Paused “Lo-fi beats to cook to” and opened “Weekend pancakes”.", false),
      ]),
    }),
  }),
  "screenshot-2-read-page": screenshotPage({
    title: "“Read me this page.”",
    sub: "Jarvis reads the page you ask about — never form fields or passwords.",
    window: browserWindow({
      width: 1120, height: 590,
      tabs: [TABS_VOICE[0], { ...TABS_VOICE[2], active: true }, { title: "Weekend pancakes", color: "#e9926b" }],
      url: "garden.example/autumn-planting", page: ARTICLE_PAGE,
      overlay: islandOverlay(ISLAND_LISTENING) + chatCard([
        bubble("Jordan", "What should I plant this week?", true),
        bubble("Jarvis", "This page suggests garlic, broad beans, spinach, radishes and onion sets — water in the morning and mulch the beds.", false),
      ]),
    }),
  }),
  "screenshot-3-private": privacyPage(1280, 800),
};

function privacyPage(w, h) {
  const box = (title, sub, icon) => `<div style="width:300px;background:#fff;border-radius:20px;padding:30px 26px;text-align:center;
      box-shadow:0 24px 60px rgba(10,6,40,.35)">
      <div style="height:72px;display:grid;place-items:center">${icon}</div>
      <div style="font-size:21px;font-weight:700;color:#211c33;margin-top:14px">${title}</div>
      <div style="font-size:15px;line-height:1.5;color:#5c5675;margin-top:8px">${sub}</div></div>`;
  const arrow = (label) => `<div style="display:flex;flex-direction:column;align-items:center;justify-content:center;gap:8px;width:120px">
      <div style="font-size:13px;color:${LAVENDER};opacity:.85;text-align:center">${label}</div>
      <div style="width:100%;height:3px;background:${LAVENDER};position:relative;opacity:.9">
        <div style="position:absolute;right:-2px;top:-6px;border-left:12px solid ${LAVENDER};border-top:7.5px solid transparent;border-bottom:7.5px solid transparent"></div></div></div>`;
  const browserIcon = `<div style="width:84px;height:62px;border-radius:9px;border:4px solid ${INDIGO};position:relative">
      <div style="height:14px;border-bottom:4px solid ${INDIGO}"></div><img src="${LOGO}" style="height:30px;position:absolute;left:50%;top:22px;margin-left:-11px"></div>`;
  const laptopIcon = `<div style="display:flex;flex-direction:column;align-items:center"><div style="width:76px;height:50px;border-radius:7px 7px 0 0;border:4px solid ${INDIGO};display:grid;place-items:center">
      <img src="${ISLAND_LISTENING}" style="width:62px"></div><div style="width:100px;height:8px;border-radius:0 0 6px 6px;background:${INDIGO}"></div></div>`;
  const serverIcon = `<div style="display:flex;flex-direction:column;gap:6px">${[0, 1, 2]
    .map(() => `<div style="width:84px;height:18px;border-radius:5px;border:4px solid ${INDIGO};position:relative"><div style="position:absolute;right:6px;top:3px;width:5px;height:5px;border-radius:50%;background:#5aa469"></div></div>`)
    .join("")}</div>`;
  return `<style>${BASE_CSS}
    body { width:${w}px; height:${h}px; background: radial-gradient(1200px 700px at 20% 0%, #5241b8 0%, ${INDIGO} 45%, ${INDIGO_DARK} 100%);
      display:flex; flex-direction:column; align-items:center; justify-content:center; }
    h1 { color:#fff; font-size:40px; font-weight:700; text-align:center; }
    .sub { color:${LAVENDER}; opacity:.85; font-size:19px; margin:12px 0 64px; text-align:center; }
    .pill { margin-top:64px; display:flex; gap:14px; }
    .pill div { color:#fff; background:rgba(255,255,255,.12); border:1px solid rgba(255,255,255,.25); border-radius:999px; padding:10px 20px; font-size:16px; }
  </style>
  <h1>Private by design.</h1>
  <div class="sub">Page text and tab titles go only to the Jarvis server you run yourself.</div>
  <div style="display:flex;align-items:stretch;gap:18px">
    ${box("Your browser", "Jarvis Browser Control reads a page only when you ask Jarvis to.", browserIcon)}
    ${arrow("native messaging")}
    ${box("Jarvis agent", "The desktop app on this computer relays the request.", laptopIcon)}
    ${arrow("your network")}
    ${box("Your Jarvis server", "Self-hosted on your own hardware. Open source.", serverIcon)}
  </div>
  <div class="pill"><div>No analytics</div><div>No third parties</div><div>Nothing stored in the browser</div></div>`;
}

function promoPage(w, h) {
  const big = w > 1000;
  return `<style>${BASE_CSS}
    body { width:${w}px; height:${h}px; overflow:hidden;
      background: radial-gradient(${w}px ${h * 1.6}px at 0% 0%, #5241b8 0%, ${INDIGO} 50%, ${INDIGO_DARK} 100%);
      display:flex; align-items:center; justify-content:center; gap:${big ? 72 : 26}px; padding:0 ${big ? 120 : 34}px; }
    img.logo { height:${big ? 340 : 168}px; flex:none; filter: drop-shadow(0 16px 30px rgba(0,0,0,.35)); }
    .t { color:#fff; font-size:${big ? 64 : 33}px; font-weight:700; line-height:1.08; letter-spacing:-.01em; }
    .s { color:${LAVENDER}; font-size:${big ? 26 : 15}px; line-height:1.4; margin-top:${big ? 20 : 12}px; opacity:.9; }
    img.island { width:${big ? 300 : 170}px; margin:${big ? "26px 0 0 -26px" : "10px 0 0 -15px"}; display:block; }
  </style><img class="logo" src="${LOGO}">
  <div><div class="t">Jarvis<br>Browser Control</div>
    <div class="s">Open, read and close tabs and control${big ? "<br>" : " "}video by voice. Private, self-hosted.</div>
    <img class="island" src="${ISLAND_SPEAKING}"></div>`;
}

const RENDERS = [
  // Chrome store icon and the extension's own icons: 96 px of art in 128 (Chrome's guidance), 40 in 48.
  { file: join(OUT, "store-icon-128.png"), w: 128, h: 128, html: iconPage(128, 96), transparent: true },
  { file: join(EXT, "icon-128.png"), w: 128, h: 128, html: iconPage(128, 96), transparent: true },
  { file: join(EXT, "icon-48.png"), w: 48, h: 48, html: iconPage(48, 40), transparent: true },
  // AMO's listing icon (it scales a 128 down to 64 and 32; this one is drawn for 64).
  { file: join(OUT, "amo-icon-64.png"), w: 64, h: 64, html: iconPage(64, 56), transparent: true },
  ...Object.entries(SHOTS).map(([name, html]) => ({ file: join(OUT, `${name}.png`), w: 1280, h: 800, html })),
  { file: join(OUT, "promo-small-440x280.png"), w: 440, h: 280, html: promoPage(440, 280) },
  { file: join(OUT, "promo-marquee-1400x560.png"), w: 1400, h: 560, html: promoPage(1400, 560) },
];

const browser = await chromium.launch({ headless: true });
try {
  mkdirSync(OUT, { recursive: true });
  for (const r of RENDERS) {
    const page = await browser.newPage({ viewport: { width: r.w, height: r.h }, deviceScaleFactor: 1 });
    await page.setContent(`<!doctype html><meta charset="utf-8">${r.html}`, { waitUntil: "load" });
    await page.evaluate(() => document.fonts.ready);
    const png = await page.screenshot({ omitBackground: !!r.transparent, animations: "disabled", caret: "hide" });
    writeFileSync(r.file, png);
    await page.close();
    console.log(`${r.w}x${r.h}  ${r.file}`);
  }
} finally {
  await browser.close();
}

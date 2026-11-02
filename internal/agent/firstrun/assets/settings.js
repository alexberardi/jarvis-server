// Local settings use the same protected origin as setup. No server credentials or K2.
"use strict";
let settingsView = null;
let settingsPatch = {};
function settingsDirty() {
  const dirty = Object.keys(settingsPatch).length > 0;
  $("settings-save").disabled = !dirty;
  $("settings-reload").textContent = dirty ? "Discard changes and reload" : "Reload saved settings";
}
function renderSettings(view) {
  settingsView = view;
  settingsPatch = {};
  const root = $("settings-fields");
  root.replaceChildren();
  let group = "", section;
  for (const f of view.fields) {
    if (f.group !== group) {
      group = f.group;
      section = document.createElement("fieldset");
      section.className = "settings-group";
      const legend = document.createElement("legend");
      legend.textContent = group;
      section.append(legend);
      root.append(section);
    }
    const row = document.createElement("div");
    row.className = "row";
    const label = document.createElement("label");
    const id = "setting-" + f.key;
    label.htmlFor = id;
    label.textContent = f.label;
    const input = document.createElement(f.type === "dictation" ? "select" : "input");
    input.id = id;
    input.name = f.key;
    const locked = view.read_only?.includes(f.key);
    input.disabled = !!locked;
    if (f.type === "dictation") {
      for (const [value, text] of [["off", "Off"], ["submit", "Before submitting"], ["all", "Before every action"]]) {
        const option = document.createElement("option"); option.value = value; option.textContent = text; input.append(option);
      }
    } else if (f.type === "boolean") {
      input.type = "checkbox"; input.checked = view.values[f.key]; input.setAttribute("role", "switch");
    } else {
      input.type = f.type === "number" ? "number" : "text";
      if (f.type === "number") input.step = "any"; // untouched values may come from older versions
    }
    if (f.type !== "boolean") { input.value = view.values[f.key]; input.required = true; }
    input.addEventListener("input", () => {
      if (f.type === "number") { input.min = f.min; input.max = f.max; input.step = f.step || "any"; }
      const value = f.type === "boolean" ? input.checked : f.type === "number" ? input.valueAsNumber : input.value;
      if (value === settingsView.values[f.key]) delete settingsPatch[f.key]; else settingsPatch[f.key] = value;
      settingsDirty(); $("settings-result").textContent = "";
    });
    if (f.type === "boolean") { label.className = "switch-row"; label.prepend(input); row.append(label); }
    else row.append(label, input);
    if (f.help) { const help = document.createElement("p"); help.id = id+"-help"; help.className = "muted"; help.textContent = f.help + (locked ? " Set by a command-line flag; change the agent launch options to edit it." : ""); input.setAttribute("aria-describedby",help.id); row.append(help); }
    section.append(row);
  }
  show("settings-loading", false); show("settings-form", true); show("settings-restart", !!view.restart_required);
  settingsDirty();
}
async function loadSettings() {
  setError("settings-error", "");
  try { renderSettings(await api("settings")); }
  catch(e) { show("settings-loading",false); setError("settings-error",e.message); show("settings-form",true); }
}
document.addEventListener("DOMContentLoaded", () => {
  $("settings-form").addEventListener("submit", async e => {
    e.preventDefault();
    if (!Object.keys(settingsPatch).length) return;
    const controls = $("settings-form").querySelectorAll("input,select,button");
    controls.forEach(el => { el.disabled = true; });
    setError("settings-error", "");
    try {
      renderSettings(await api("settings", {settings:settingsPatch}));
      $("settings-result").textContent = "Saved. Voice changes apply between turns or when listening starts.";
    } catch(e) { setError("settings-error",e.message); }
    finally { controls.forEach(el => { el.disabled = !!settingsView?.read_only?.includes(el.name); }); settingsDirty(); }
  });
  $("settings-reload").addEventListener("click",loadSettings);
  $("settings-restart").addEventListener("click", async () => {
    $("settings-restart").disabled = true;
    try { await api("restart", {}); await loadSettings(); $("settings-result").textContent = "Jarvis restarted."; }
    catch(e) { setError("settings-error",e.message); }
    finally { $("settings-restart").disabled = false; }
  });
  void loadSettings();
});

// The toolbar popup shows whether the extension has website access and, when it hasn't, a
// "Turn on" button that asks the browser for it (permissions.request, inside the click).
import { test } from "node:test";
import assert from "node:assert/strict";
import { fakeAPI, loadPopup, settle } from "./fake.mjs";

test("website access on: says so, no button", async () => {
  const { api, state } = fakeAPI({ access: true });
  const { els } = loadPopup(api);
  await settle();
  assert.equal(els["access-state"].textContent, "on");
  assert.equal(els["access"].hidden, false);
  assert.equal(els["access-on"].hidden, true);
  assert.equal(state.requests.length, 0);
});

test("website access off: says so, Turn on requests <all_urls> in the click", async () => {
  const { api, state } = fakeAPI({ access: false });
  const { els } = loadPopup(api);
  await settle();
  assert.equal(els["access-state"].textContent, "off");
  assert.equal(els["access-on"].hidden, false);
  assert.equal(els["access-hint"].hidden, false);

  els["access-on"].click();
  // The request is made synchronously in the click handler (the user gesture Firefox needs).
  assert.deepEqual(state.requests, [{ origins: ["<all_urls>"] }]);
  await settle();
  assert.equal(els["access-state"].textContent, "on");
  assert.equal(els["access-on"].hidden, true);
});

test("Turn on refused: stays off with the button", async () => {
  const { api, state } = fakeAPI({ access: false, deny: true });
  const { els } = loadPopup(api);
  await settle();
  els["access-on"].click();
  await settle();
  assert.equal(state.requests.length, 1);
  assert.equal(els["access-state"].textContent, "off");
  assert.equal(els["access-on"].hidden, false);
});

test("the agent state still shows beside it", async () => {
  const { api } = fakeAPI({ access: false });
  const { els } = loadPopup(api, "missing");
  await settle();
  assert.equal(els["title"].textContent, "Desktop agent not installed");
  assert.equal(els["access-state"].textContent, "off");
});

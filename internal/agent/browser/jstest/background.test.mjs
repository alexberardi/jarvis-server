// background.js reports website access (the <all_urls> host permission, which Firefox lets the
// user turn off) to the agent: in hello, and as an "access" event when it changes.
import { test } from "node:test";
import assert from "node:assert/strict";
import { fakeAPI, loadBackground, settle } from "./fake.mjs";

const hello = (state) => state.posted.find((m) => m.type === "hello");
const accessEvents = (state) => state.posted.filter((m) => m.type === "event" && m.event === "access");

for (const access of [true, false]) {
  test(`hello carries website_access ${access}`, async () => {
    const { api, state } = fakeAPI({ access });
    loadBackground(api);
    await settle();
    const h = hello(state);
    assert.ok(h, "no hello posted");
    assert.equal(h.website_access, access);
    assert.equal(h.browser, "Firefox");
  });
}

test("hello leaves website_access out when the browser can't say", async () => {
  const { api, state } = fakeAPI();
  state.throwContains = true;
  loadBackground(api);
  await settle();
  assert.ok(hello(state));
  assert.equal("website_access" in hello(state), false);
});

test("permissions.onRemoved and onAdded post an access event", async () => {
  const { api, state } = fakeAPI({ access: true });
  loadBackground(api);
  await settle();
  assert.equal(api.permissions.onRemoved.listeners.length, 1);
  assert.equal(api.permissions.onAdded.listeners.length, 1);

  state.access = false;
  api.permissions.onRemoved.fire({ origins: ["<all_urls>"], permissions: [] });
  await settle();
  assert.deepEqual(accessEvents(state).map((e) => e.website_access), [false]);

  state.access = true;
  api.permissions.onAdded.fire({ origins: ["<all_urls>"], permissions: [] });
  await settle();
  assert.deepEqual(accessEvents(state).map((e) => e.website_access), [false, true]);
});

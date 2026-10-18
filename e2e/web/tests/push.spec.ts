// Push notifications: each event that sends one reaches the fake relay (tools/fakerelay) with
// the right title, body, data and target tokens, and the inbox item a push names is the one the
// web inbox opens. Each test has its own household and devices, and every title is unique: the
// server drops an identical push within 60 s (notifications DedupWindow).
// docs/decisions/2026-10-18-e2e-push.md lists the events and which test covers each.
import { call, createInboxItem, appHeaders, inbox, must, newUser, unique, useScript } from '../helpers/api'
import { env } from '../helpers/env'
import { expect, test } from '../helpers/fixtures'
import { activeTokens, nodeOf, nodeWithId, pushesTo, pushTo, registerDevice } from '../helpers/push'

const cc = () => env('SBX_CC')

test.beforeAll(() => useScript('e2e/llm/web.json'))

test('a node timer, reminder and alert each push to the phone, and the push opens its inbox item', async ({ asUser }) => {
  const u = await newUser('pushnode')
  const phone = await registerDevice(u)
  const node = await nodeOf(u)
  const page = await asUser(u)
  for (const category of ['timer', 'reminder', 'alert']) {
    const title = unique(`${category} done`)
    const r = await must('POST', `${cc()}/api/v0/node/inbox-item`, {
      headers: node,
      body: { title, summary: `The ${category} went off`, body: `The ${category} went off.`, category,
        create_push_notification: true, user_id: u.id, target_type: 'user' },
    })
    expect(r.sent).toBe(true)
    const p = await pushTo(phone, title)
    expect(p).toMatchObject({ tokens: [phone], body: `The ${category} went off`, priority: 'high', household_id: u.householdId,
      data: { type: category, inbox_item_id: r.id } })

    // The push's inbox_item_id is what the phone opens: the same item in the web inbox.
    await page.goto(`/inbox/${p.data.inbox_item_id}`)
    await expect(page.getByRole('heading', { name: title })).toBeVisible()
  }
})

test('a node push (a reminder) is a high-priority confirmation card', async () => {
  const u = await newUser('pushconfirm')
  const phone = await registerDevice(u)
  const title = unique('Take the bins out')
  const r = await must('POST', `${cc()}/api/v0/node/push-notification`, {
    headers: await nodeOf(u),
    body: { title, body: 'It is bin night', user_id: u.id, target_type: 'user' },
  })
  expect(r.sent).toBe(true)
  const p = await pushTo(phone, title)
  expect(p).toMatchObject({ tokens: [phone], body: 'It is bin night', priority: 'high',
    data: { type: 'confirmation', inbox_item_id: r.inbox_item_id } })
})

test('send_link pushes an open_url with the link', async () => {
  const u = await newUser('pushlink')
  const phone = await registerDevice(u)
  const title = unique('Recipe link')
  const url = 'https://example.com/pasta'
  const r = await must('POST', `${cc()}/api/v0/node/send-link`, {
    headers: await nodeOf(u),
    body: { user_id: u.id, url, title, body: 'Open the pasta recipe' },
  })
  expect(r.sent).toBe(true)
  const p = await pushTo(phone, title)
  expect(p).toMatchObject({ tokens: [phone], body: 'Open the pasta recipe', priority: 'high',
    data: { type: 'open_url', url, household_id: u.householdId } })
  expect(p.data.inbox_item_id).toBeTruthy()
})

test('a household push reaches every phone in the household and no other household', async () => {
  const u = await newUser('pushhh')
  const other = await newUser('pushother')
  const phone = await registerDevice(u)
  const tablet = await registerDevice(u)
  const strangersPhone = await registerDevice(other)
  const title = unique('Front door left open')
  await must('POST', `${cc()}/api/v0/node/inbox-item`, {
    headers: await nodeOf(u),
    body: { title, summary: 'Since 10 minutes', category: 'alert', create_push_notification: true, target_type: 'household' },
  })
  const p = await pushTo(phone, title)
  expect(p.tokens.sort()).toEqual([phone, tablet].sort())
  expect(p.household_id).toBe(u.householdId)
  expect(await pushesTo(strangersPhone)).toEqual([])
})

test('a service notify and notify batch push; an inbox item without a push does not', async () => {
  const u = await newUser('pushapp')
  const phone = await registerDevice(u)
  const node = await nodeOf(u)
  const quiet = unique('Just for the inbox')
  // No push asked for: an inbox item from a node, and one posted by a service.
  await must('POST', `${cc()}/api/v0/node/inbox-item`, { headers: node, body: { title: quiet, category: 'general', user_id: u.id } })
  await createInboxItem(u, { title: unique('Research ready, no push') })

  const one = unique('Deep research done')
  const d = await must('POST', `${env('SBX_NOTIFICATIONS')}/api/v0/notify`, {
    headers: await appHeaders(),
    body: { target_type: 'user', target_id: String(u.id), title: one, body: 'Heat pumps', data: { type: 'deep_research', inbox_item_id: 'x-1' },
      priority: 'high', category: 'deep_research' },
  })
  expect(d.delivery_status).toBe('pending')
  expect(await pushTo(phone, one)).toMatchObject({ body: 'Heat pumps', priority: 'high', data: { type: 'deep_research', inbox_item_id: 'x-1' } })

  const a = unique('Batch one')
  const b = unique('Batch two')
  await must('POST', `${env('SBX_NOTIFICATIONS')}/api/v0/notify/batch`, {
    headers: await appHeaders(),
    body: { notifications: [a, b].map((title) => ({ target_type: 'household', target_id: u.householdId, title, body: '' })) },
  })
  expect((await pushTo(phone, a)).priority).toBe('default')
  await pushTo(phone, b)

  // Everything sent after the quiet items has arrived, and they never did.
  expect((await pushesTo(phone)).map((p) => p.title)).toEqual([one, a, b])
})

test('a phone the relay calls unregistered stops getting pushes', async () => {
  const u = await newUser('pushprune')
  const phone = await registerDevice(u)
  const gone = await registerDevice(u, 'fail:')
  expect((await activeTokens(u)).sort()).toEqual([gone, phone].sort())
  const title = unique('Laundry done')
  await must('POST', `${cc()}/api/v0/node/push-notification`, {
    headers: await nodeOf(u),
    body: { title, body: 'The washer finished', user_id: u.id, target_type: 'user' },
  })
  expect((await pushTo(phone, title)).tokens.sort()).toEqual([gone, phone].sort())
  // DeviceNotRegistered deactivates the token; the next push goes to the live phone only.
  await expect.poll(() => activeTokens(u)).toEqual([phone])
  const next = unique('Dryer done')
  await must('POST', `${cc()}/api/v0/node/push-notification`, {
    headers: await nodeOf(u),
    body: { title: next, body: 'The dryer finished', user_id: u.id, target_type: 'user' },
  })
  expect((await pushTo(phone, next)).tokens).toEqual([phone])
})

test('signing a phone out of push stops its pushes', async () => {
  const u = await newUser('pushlogout')
  const phone = await registerDevice(u)
  const r = await call('DELETE', `${env('SBX_NOTIFICATIONS')}/api/v0/tokens`, { token: u.token, body: { push_token: phone } })
  expect(r.status).toBeLessThan(300)
  const node = await nodeOf(u)
  const kept = await registerDevice(u)
  const title = unique('After sign-out')
  await must('POST', `${cc()}/api/v0/node/push-notification`, { headers: node, body: { title, body: '', user_id: u.id, target_type: 'user' } })
  expect((await pushTo(kept, title)).tokens).toEqual([kept])
  expect(await pushesTo(phone)).toEqual([])
})

test('a signal watch made in chat pushes its alert when the signal comes', async () => {
  const u = await newUser('pushwatch')
  const phone = await registerDevice(u)
  const node = await nodeWithId(u)
  await must('PUT', `${cc()}/api/v0/mobile/household/${u.householdId}/settings/watches.enabled`, { token: u.token, body: { value: true } })

  // The scripted model (e2e/llm/web.json) answers this with create_watch on appt.upcoming.
  const chat = await must<string>('POST', `${cc()}/api/v0/mobile/chat`, {
    token: u.token,
    body: { message: 'tell me when my e2e dentist appointment comes up', node_id: node.id, household_id: u.householdId },
  })
  expect(chat).toContain("I'll tell you when it comes up.")

  // The node sees the appointment coming up; the watch fires and pushes to the user's phone.
  const appt = unique('E2E dentist')
  await must('POST', `${cc()}/api/v0/signals`, {
    headers: node.headers,
    body: { signal: { kind: 'appt.upcoming', source_key: appt, summary: `${appt} soon` }, data: { title: appt } },
  })
  const p = await pushTo(phone, '👀 E2E dentist appointment')
  expect(p).toMatchObject({ tokens: [phone], household_id: u.householdId, data: { type: 'watch_alert' } })
  expect(p.body).toContain(appt)
  const item = (await inbox(u.token)).find((i) => i.id === p.data.inbox_item_id)
  expect(item?.title).toBe('👀 E2E dentist appointment')
})

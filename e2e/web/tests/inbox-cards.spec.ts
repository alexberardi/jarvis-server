// Inbox cards (docs/webapp/03 step 2): editable fields, a server-plane tap through the gateway to
// cc and back, expiry, and the watch card.
import { createInboxItem, newUser } from '../helpers/api'
import { expect, test } from '../helpers/fixtures'

const callCard = (meta: Record<string, unknown> = {}) => ({
  title: 'Call Lakeview Dental?',
  body: 'Jarvis will call to book a cleaning.',
  category: 'confirmation',
  metadata: {
    editor_schema: 2,
    editable_fields: [
      { label: 'Phone number', initial: '+1 (312) 847-1928', data_key: 'dialed_number', input_type: 'tel' },
      { label: 'Details', initial: 'Book a cleaning next week', data_key: 'details', input_type: 'multiline' },
    ],
    interactive_elements: [
      { id: 'go', label: 'Call', command: 'make_phone_call', callback: 'confirm_call', target: 'server', data: { session_id: 'nope', dialed_number: '', details: '' } },
      { id: 'no', label: 'Cancel', command: 'make_phone_call', callback: 'cancel_call', target: 'server', data: { session_id: 'nope' } },
    ],
    ...meta,
  },
})

test('a card sends its edited fields to cc and says why the job failed', async ({ asUser }) => {
  const u = await newUser('cards')
  const card = await createInboxItem(u, callCard())
  const page = await asUser(u)
  await page.goto(`/inbox/${card.id}`)

  const number = page.getByLabel('Phone number')
  await expect(number).toHaveValue('+1 (312) 847-1928')
  await number.fill('')
  await page.getByRole('button', { name: 'Call' }).click()
  await expect(page.getByRole('alert')).toHaveText('Phone number is required')

  // An unknown session: cc runs the handler and the job fails with its message.
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.getByText('Call plan not found')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Cancel' })).toBeEnabled()
})

test('an expired card has its buttons off', async ({ asUser }) => {
  const u = await newUser('cardsexp')
  const card = await createInboxItem(u, callCard({ expires_at: '2020-01-01T00:00:00Z' }))
  const page = await asUser(u)
  await page.goto(`/inbox/${card.id}`)
  await expect(page.getByText('This card has expired.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Call' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Cancel' })).toBeDisabled()
})

test('a fired watch shows the change and its page', async ({ asUser }) => {
  const u = await newUser('cardswatch')
  const card = await createInboxItem(u, {
    title: 'Price dropped',
    category: 'watch',
    metadata: { card_type: 'watch_alert', looking_for: 'Under $400', value: '$379', previous: '$429', source_url: 'https://shop.example/ps5', source_label: 'Northwind Games', state: 'active' },
  })
  const page = await asUser(u)
  await page.goto(`/inbox/${card.id}`)
  await expect(page.getByText('$429 → $379')).toBeVisible()
  await expect(page.getByRole('link', { name: /Open Northwind Games/ })).toHaveAttribute('href', 'https://shop.example/ps5')
})

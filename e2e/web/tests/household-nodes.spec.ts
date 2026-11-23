// The household's nodes for every member (mobile's Nodes tab): a node of the household shows up
// with its room and settings, and another household's doesn't. (Removal needs an offline node: a
// just-registered one counts as online, so the admin Nodes page's tests cover it.)
import { newUser, provisionNode, unique } from '../helpers/api'
import { expect, test } from '../helpers/fixtures'

test('the household’s nodes are listed with their settings', async ({ asUser }) => {
  const u = await newUser('nodes')
  const other = await newUser('nodes-other')
  const id = unique('porch')
  await provisionNode(u.token, u.householdId, { id, name: 'Porch node', room: 'porch' })
  await provisionNode(other.token, other.householdId, { id: unique('elsewhere'), name: 'Elsewhere', room: 'attic' })
  const page = await asUser(u)
  await page.goto('/nodes')

  const card = page.getByTestId(`node-${id}`)
  await expect(card.getByRole('heading', { name: 'Porch node' })).toBeVisible()
  await expect(card.getByText('Room: porch')).toBeVisible()
  await expect(card.getByRole('button', { name: 'Settings' })).toBeVisible()
  await expect(page.getByText('Elsewhere')).toHaveCount(0)
  // A member's page has no Forge test link: that page is the admin's.
  await expect(card.getByRole('link', { name: 'Test a Forge command' })).toHaveCount(0)
})

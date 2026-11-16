// Interactive lists on the web (docs/webapp/03 step 3): rows, defaults and quantities, a gated
// row whose node can't be asked, and an action whose callback cc refuses, with Retry.
import { createInboxItem, newUser } from '../helpers/api'
import { expect, test } from '../helpers/fixtures'

test('an interactive list renders its rows and says why an action failed', async ({ asUser }) => {
  const u = await newUser('lists')
  const item = await createInboxItem(u, {
    title: 'Shopping list',
    category: 'interactive_list',
    metadata: {
      type: 'interactive_list',
      version: 1,
      command_name: 'export_shopping_list',
      node_id: 'no-such-node',
      sections: [
        {
          title: 'Ready for Walmart',
          rows: [
            { key: 'milk', label: 'Whole milk', control: 'checkbox_stepper', default: { selected: true, quantity: 2 } },
            {
              key: 'oats',
              label: 'Rolled oats',
              control: 'checkbox',
              disabled_caption: 'No Walmart item yet',
              requires_record_field: { command_name: 'export_shopping_list', field: 'walmart_item_id', field_label: 'ID' },
            },
          ],
        },
      ],
      actions: [{ label: 'Add {n} to cart', callback: 'add_to_cart', style: 'primary' }],
    },
  })
  const page = await asUser(u)
  await page.goto(`/inbox/${item.id}`)

  await expect(page.getByLabel('Whole milk', { exact: true })).toBeChecked()
  await expect(page.getByLabel('Quantity of Whole milk')).toHaveValue('2')
  // The node's records can't be read: the gated row stays off, with its reason.
  await expect(page.getByLabel('Rolled oats', { exact: true })).toBeDisabled()
  await expect(page.getByText('No Walmart item yet')).toBeVisible()

  await page.getByRole('button', { name: 'One more Whole milk' }).click()
  await page.getByRole('button', { name: 'Add 1 to cart' }).click()
  await expect(page.getByRole('alert')).toContainText('not found')
  await expect(page.getByRole('button', { name: 'Retry' })).toBeEnabled()
})

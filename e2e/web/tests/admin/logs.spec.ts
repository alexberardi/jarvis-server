// /admin/logs: lines the test sends through jarvis-logs' ingest are found with the page's
// filters (module, level, text), and the filters leave the rest out.
import { postLog, unique } from '../../helpers/api'
import { stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

test.describe('admin logs', () => {
  test('a line sent by a service shows up, and the module, level and text filters find it', async ({ page }) => {
    const service = unique('e2e-logs')
    const word = unique('needle')
    await postLog({ service, level: 'ERROR', message: `the ${word} failed`, context: { e2e: true } })
    await postLog({ service, level: 'INFO', message: `the ${word} started` })
    await postLog({ service, level: 'INFO', message: 'an unrelated line' })

    await page.goto('/admin/logs')
    const entries = page.getByRole('list', { name: 'Log entries' })
    await page.getByLabel('Module').selectOption(service)
    await expect(entries.getByRole('listitem')).toHaveCount(3)
    for (const li of await entries.getByRole('listitem').all()) await expect(li).toContainText(service)

    await page.getByLabel('Search logs').fill(word)
    await expect(entries.getByRole('listitem')).toHaveCount(2)
    await expect(entries).not.toContainText('an unrelated line')

    await page.getByLabel('Minimum level').selectOption('ERROR')
    await expect(entries.getByRole('listitem')).toHaveCount(1)
    const line = entries.getByRole('listitem').first()
    await expect(line).toContainText('ERROR')
    await expect(line).toContainText(`the ${word} failed`)
    // Its context opens under it.
    await line.getByRole('button').click()
    await expect(line).toContainText('"e2e": true')

    await page.getByLabel('Search logs').fill(unique('nothing'))
    await expect(page.getByText('No log entries match.')).toBeVisible()
  })
})

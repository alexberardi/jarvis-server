// /admin/models: the catalog against the server's, a job's "Set up" narrowing it, a model
// assigned to a label (saved on the server, survives a reload), and installs that never start.
import { must, tokenOf } from '../../helpers/api'
import { env, stateFile } from '../../helpers/env'
import { expect, test } from '../../helpers/fixtures'

test.use({ storageState: stateFile('superuser') })

const LLM = () => `${env('SBX_ADMIN')}/api/llm/v1/models`
const get = (path: string) => must('GET', `${LLM()}${path}`, { token: tokenOf('superuser') })

// The kinds the catalog lists (CatalogList.tsx KIND_GROUPS); vision projectors come with their model.
const LISTED = ['llm', 'embedding', 'stt', 'tts', 'speaker']

// Each setup job's model kind (web/admin/src/components/setup/jobs.ts).
const JOB_KIND: Record<string, string> = { llm: 'llm', stt: 'stt', voice: 'tts', speaker: 'speaker', memory: 'embedding' }

interface CatalogEntry {
  id: string
  display: string
  kind: string
}

test.describe('admin models', () => {
  test('lists the whole catalog; Install asks first and nothing downloads', async ({ page }) => {
    const catalog: CatalogEntry[] = (await get('/catalog')).models.filter((m: CatalogEntry) => LISTED.includes(m.kind))
    expect(catalog.length).toBeGreaterThan(0)
    const installs: string[] = []
    page.on('request', (r) => {
      if (r.method() === 'POST' && /\/api\/llm\/v1\/models\/(install|hf)/.test(r.url())) installs.push(r.url())
    })
    await page.goto('/admin/models')
    await page.getByRole('tab', { name: 'Catalog' }).click()
    const section = page.getByRole('region', { name: 'Catalog' })
    for (const m of catalog) await expect(section.getByTestId(`catalog-${m.id}`)).toContainText(m.display)

    const first = section.getByTestId(`catalog-${catalog[0].id}`)
    await first.getByRole('button', { name: 'Install', exact: true }).click()
    await expect(first.getByRole('button', { name: `Install ${catalog[0].display}` })).toBeVisible()
    await first.getByRole('button', { name: 'Close' }).click()
    await expect(first.getByRole('button', { name: `Install ${catalog[0].display}` })).toHaveCount(0)
    expect(installs).toEqual([])
  })

  test("a job's Set up narrows the catalog to its models, and Show all brings the rest back", async ({ page }) => {
    const catalog: CatalogEntry[] = (await get('/catalog')).models.filter((m: CatalogEntry) => LISTED.includes(m.kind))
    await page.goto('/admin/models')
    const setUp = page.getByRole('region', { name: 'Jobs' }).getByRole('button', { name: /^Set up / })
    // The sandbox never sets up stt or voice (no downloads), so a Set up button is always there.
    await expect(setUp.first()).toBeVisible()
    await setUp.first().click()
    const job = new URL(page.url()).searchParams.get('job')!
    const kind = JOB_KIND[job]
    expect(kind, `job ${job}`).toBeTruthy()
    await expect(page.getByText('Showing models for')).toBeVisible()
    const section = page.getByRole('region', { name: 'Catalog' })
    for (const m of catalog) {
      await expect(section.getByTestId(`catalog-${m.id}`)).toHaveCount(m.kind === kind ? 1 : 0)
    }
    await page.getByRole('button', { name: 'Show all' }).click()
    await expect(page).not.toHaveURL(/job=/)
    for (const m of catalog) await expect(section.getByTestId(`catalog-${m.id}`)).toHaveCount(1)
  })

  test('assigning a model to a label saves it, across a reload', async ({ page }) => {
    // Nothing is installed on the sandbox (no downloads), so the label to assign is a remote
    // one: live runs the scripted model, which answers whatever model name it is sent.
    // Background shares live's engine, so it follows.
    const label = (b: any, name: string) => b.labels.find((l: { label: string }) => l.label === name)
    const before = label(await get('/labels'), 'live').config
    expect(before.engine).toBe('remote')
    const model = `scripted-e2e-${Date.now().toString(36)}`
    try {
      await page.goto('/admin/models')
      const card = page.getByTestId('label-live')
      await card.getByLabel('Model name').fill(model)
      await card.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByText('Live (voice replies) saved; its engine restarts if needed')).toBeVisible()
      await expect(card.getByRole('button', { name: 'Save' })).toHaveCount(0)

      const after = await get('/labels')
      expect(label(after, 'live').config.remote_model).toBe(model)
      expect(label(after, 'live').endpoint.model).toBe(model)
      expect(label(after, 'background').config).toMatchObject({ shared_with: 'live', remote_model: model })
      await page.reload()
      await expect(page.getByTestId('label-live').getByLabel('Model name')).toHaveValue(model)
    } finally {
      await must('PUT', `${LLM()}/labels`, { token: tokenOf('superuser'), body: { live: { remote_model: before.remote_model } } })
    }
  })

  test('installing from Hugging Face', async () => {
    test.skip(true, 'an install downloads from huggingface.co; there is no fake Hugging Face for the sandbox yet (E2E 7/9 fakes HA and pairing only)')
  })
})

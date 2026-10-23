// /admin/models: the catalog against the server's, a job's "Set up" narrowing it, a model
// assigned to a label (saved on the server, survives a reload), installs that never start, and
// the optional cloud slot picked for computer control (with what it sends where), and speech to
// text sent to a remote provider (its key never read back).
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

  test('the cloud slot, set up and picked for computer control, says what it sends where', async ({ page }) => {
    // Nothing calls the cloud endpoint here: no computer task runs, and the URL is never dialled.
    const settingURL = `${env('SBX_ADMIN')}/api/settings/cc/computer.model`
    const superuser = { token: tokenOf('superuser') }
    const cloudOf = (b: any) => b.labels.find((l: { label: string }) => l.label === 'cloud')
    expect(cloudOf(await get('/labels'))).toMatchObject({ state: 'not_configured', config: { remote_url: '' } })
    try {
      await page.goto('/admin/models')
      const select = page.getByLabel('Computer control')
      await expect(select).toHaveValue('auto')
      await expect(page.getByRole('option', { name: 'Cloud (not set up)' })).toBeDisabled()

      const card = page.getByTestId('label-cloud')
      await card.getByLabel('Base URL').fill('https://api.anthropic.com')
      await card.getByLabel('Model name').fill('claude-sonnet-5-5')
      await card.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByText('Cloud (optional) saved; its engine restarts if needed')).toBeVisible()
      expect(cloudOf(await get('/labels'))).toMatchObject({ state: 'remote', config: { remote_url: 'https://api.anthropic.com' } })

      await select.selectOption('cloud')
      const notice = page.getByText(
        'Page contents and screenshots of your browser are sent to Anthropic (api.anthropic.com) while a task runs.',
      )
      await expect(notice).toBeVisible()
      await expect.poll(async () => (await must('GET', `${env('SBX_ADMIN')}/api/settings/?service=cc`, superuser))
        .services.flatMap((s: any) => s.settings).find((s: any) => s.key === 'computer.model')?.value).toBe('cloud')
      await page.reload()
      await expect(page.getByLabel('Computer control')).toHaveValue('cloud')
      await expect(notice).toBeVisible()
      await page.getByLabel('Computer control').selectOption('auto')
      await expect(notice).toHaveCount(0)
    } finally {
      await must('PUT', settingURL, { ...superuser, body: { value: 'auto' } })
      await must('PUT', `${LLM()}/labels`, { ...superuser, body: { cloud: { remote_url: '', remote_model: '' } } })
    }
  })

  test('speech to text set to a remote provider saves, and no read gives its key back', async ({ page }) => {
    // Nothing transcribes here, so the URL (a closed local port) is never dialled.
    const superuser = { token: tokenOf('superuser') }
    const sttOf = (b: any) => b.labels.find((l: { label: string }) => l.label === 'stt')
    const before = sttOf(await get('/labels')).config
    const key = `sk-e2e-${Date.now().toString(36)}`
    try {
      await page.goto('/admin/models')
      const card = page.getByTestId('label-stt')
      await card.getByLabel('Runs').selectOption('remote')
      await expect(card.getByText(/The audio of every voice turn leaves this machine/)).toBeVisible()
      await card.getByLabel('Base URL').fill('http://127.0.0.1:9/v1')
      await card.getByLabel('Model name').fill('whisper-large-v3-turbo')
      await card.getByLabel('API key').fill(key)
      await card.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByText('Speech to text saved; its engine restarts if needed')).toBeVisible()

      const labels = await get('/labels')
      expect(sttOf(labels)).toMatchObject({
        state: 'remote',
        config: { engine: 'remote', remote_url: 'http://127.0.0.1:9/v1', remote_model: 'whisper-large-v3-turbo', remote_api_effective: 'openai' },
      })
      expect(JSON.stringify(labels)).not.toContain(key)
      const settings = await must('GET', `${env('SBX_ADMIN')}/api/settings/?service=llm`, superuser)
      expect(JSON.stringify(settings)).not.toContain(key)
      expect(settings.services.flatMap((s: any) => s.settings).find((s: any) => s.key === 'stt.remote_api_key')?.value).toBe('********')

      await page.reload()
      await expect(page.getByTestId('label-stt').getByLabel('Runs')).toHaveValue('remote')
      await expect(page.getByTestId('label-stt').getByLabel('API key')).toHaveValue('')
    } finally {
      await must('PUT', `${LLM()}/labels`, {
        ...superuser,
        body: { stt: { engine: before.engine, remote_url: '', remote_model: '', remote_api_key: '' } },
      })
    }
  })

  test('installing from Hugging Face', async () => {
    test.skip(true, 'an install downloads from huggingface.co; there is no fake Hugging Face for the sandbox yet (E2E 7/9 fakes HA and pairing only)')
  })
})

// The Speech section (docs/llm/07-remote-speech.md §4): presets fill the fields, the TTS card
// saves through /api/settings with the key write-only, and the test buttons round-trip through
// a fake OpenAI-compatible provider on a local port — never a real one.
test.describe('admin speech section', () => {
  test('an stt preset fills URL, API and model, which stay editable', async ({ page }) => {
    await page.goto('/admin/models')
    const card = page.getByTestId('label-stt')
    await card.getByLabel('Runs').selectOption('remote')
    await card.getByLabel('Provider', { exact: true }).selectOption('groq')
    await expect(card.getByLabel('Base URL')).toHaveValue('https://api.groq.com/openai/v1')
    await expect(card.getByLabel('Model name')).toHaveValue('whisper-large-v3-turbo')
    await expect(card.getByLabel('API', { exact: true })).toHaveValue('openai')
    await expect(card.getByText(/Groq bills each request/)).toBeVisible()
    await card.getByLabel('Base URL').fill('http://127.0.0.1:9/v1')
    await expect(card.getByLabel('Base URL')).toHaveValue('http://127.0.0.1:9/v1')
    // Never saved: the next test starts from the server's state.
  })

  test('speaking saves a remote provider through settings, and no read gives its key back', async ({ page }) => {
    const superuser = { token: tokenOf('superuser') }
    const key = `sk-e2e-${Date.now().toString(36)}`
    try {
      await page.goto('/admin/models')
      const card = page.getByTestId('speech-card')
      await card.getByLabel('Runs').selectOption('remote')
      await expect(card.getByText(/Everything Jarvis says leaves this machine/)).toBeVisible()
      await card.getByLabel('Provider', { exact: true }).selectOption('openai')
      await expect(card.getByLabel('Base URL')).toHaveValue('https://api.openai.com/v1')
      await expect(card.getByLabel('Model name')).toHaveValue('gpt-4o-mini-tts')
      await card.getByLabel('API key').fill(key)
      await card.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByText('Speaking saved')).toBeVisible()

      const settings = await must('GET', `${env('SBX_ADMIN')}/api/settings/?service=tts`, superuser)
      const all = settings.services.flatMap((s: any) => s.settings)
      expect(all.find((s: any) => s.key === 'tts.provider')?.value).toBe('remote')
      expect(all.find((s: any) => s.key === 'tts.remote_model')?.value).toBe('gpt-4o-mini-tts')
      expect(JSON.stringify(settings)).not.toContain(key)
      expect(all.find((s: any) => s.key === 'tts.remote_api_key')?.value).toBe('********')

      await page.reload()
      const after = page.getByTestId('speech-card')
      await expect(after.getByLabel('Runs')).toHaveValue('remote')
      await expect(after.getByLabel('API key')).toHaveValue('')
    } finally {
      for (const [k, v] of [
        ['tts.provider', 'kokoro'],
        ['tts.remote_url', ''],
        ['tts.remote_model', ''],
        ['tts.remote_api_key', ''],
        ['tts.remote_voice', ''],
      ] as const) {
        await must('PUT', `${env('SBX_ADMIN')}/api/settings/tts/${k}`, { ...superuser, body: { value: v } })
      }
    }
  })

  test('the test buttons round-trip through a fake provider and say which engine answered', async ({ page }) => {
    const superuser = { token: tokenOf('superuser') }
    const sttOf = (b: any) => b.labels.find((l: { label: string }) => l.label === 'stt')
    const before = sttOf(await get('/labels')).config
    // A fake OpenAI-compatible speech server on a local port (doc §5: never a real provider).
    const http = await import('node:http')
    const fake = http.createServer((req, res) => {
      if (req.method === 'POST' && req.url?.endsWith('/audio/transcriptions')) {
        req.resume()
        req.on('end', () => {
          res.setHeader('Content-Type', 'application/json')
          res.end(JSON.stringify({ text: 'tell me a joke' }))
        })
      } else if (req.method === 'POST' && req.url?.endsWith('/audio/speech')) {
        req.resume()
        req.on('end', () => {
          res.setHeader('Content-Type', 'audio/pcm')
          res.end(Buffer.alloc(24000)) // half a second of 24 kHz s16le silence
        })
      } else {
        res.statusCode = 404
        res.end()
      }
    })
    await new Promise<void>((ok) => fake.listen(0, '127.0.0.1', ok))
    const url = `http://127.0.0.1:${(fake.address() as import('node:net').AddressInfo).port}`
    try {
      await must('PUT', `${LLM()}/labels`, {
        ...superuser,
        body: { stt: { engine: 'remote', remote_url: url, remote_model: 'whisper-1', remote_api: 'openai', remote_api_key: 'k' } },
      })
      for (const [k, v] of [
        ['tts.provider', 'remote'],
        ['tts.remote_url', url],
        ['tts.remote_model', 'tts-fake'],
        ['tts.remote_voice', 'alloy'],
      ] as const) {
        await must('PUT', `${env('SBX_ADMIN')}/api/settings/tts/${k}`, { ...superuser, body: { value: v } })
      }

      await page.goto('/admin/models')
      const stt = page.getByTestId('label-stt')
      await stt.getByRole('button', { name: 'Test transcription' }).click()
      await expect(stt.getByTestId('stt-test').getByRole('status')).toHaveText(
        /Heard “tell me a joke” in .*, by the remote provider\. The clip says “tell me a joke”\./,
      )

      const speech = page.getByTestId('speech-card')
      await speech.getByRole('button', { name: 'Play test sentence' }).click()
      await expect(speech.getByTestId('tts-test').getByRole('status')).toHaveText(
        /Rendered in .*, by the remote provider\./,
      )
    } finally {
      await new Promise<void>((ok) => fake.close(() => ok()))
      await must('PUT', `${LLM()}/labels`, {
        ...superuser,
        body: { stt: { engine: before.engine, remote_url: '', remote_model: '', remote_api_key: '' } },
      })
      for (const [k, v] of [
        ['tts.provider', 'kokoro'],
        ['tts.remote_url', ''],
        ['tts.remote_model', ''],
        ['tts.remote_api_key', ''],
        ['tts.remote_voice', ''],
      ] as const) {
        await must('PUT', `${env('SBX_ADMIN')}/api/settings/tts/${k}`, { ...superuser, body: { value: v } })
      }
    }
  })
})

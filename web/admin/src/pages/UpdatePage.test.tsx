import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { apiClient } from '@/api/client'
import { updateStatus, type UpdateInfo } from '@/api/update'
import { resetFeatures } from '@/lib/features'
import UpdateBanner from '@/components/dashboard/UpdateBanner'
import UpdatePage from './UpdatePage'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(), loading: vi.fn() }),
}))

function info(over: Partial<UpdateInfo>): UpdateInfo {
  return {
    updates_enabled: true,
    checked: true,
    reason: null,
    checked_at: '2026-10-07T12:00:00Z',
    current_version: 'v0.1.0',
    latest_version: 'v0.1.0',
    update_available: false,
    up_to_date: false,
    prerelease: false,
    release_url: null,
    release_notes: null,
    published_at: null,
    platform: 'linux-amd64',
    asset: null,
    checksums_url: null,
    install_command: null,
    install_hint: 'Download the release, verify it against SHA256SUMS and replace the binary.',
    ...over,
  }
}

let current: UpdateInfo
const get = vi.spyOn(apiClient, 'get')
const post = vi.spyOn(apiClient, 'post')
const put = vi.spyOn(apiClient, 'put')

function renderPage(withBanner = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        {withBanner && <UpdateBanner />}
        <UpdatePage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  resetFeatures()
  get.mockImplementation(async (url: string) => {
    if (url === '/api/update') return { data: current }
    if (url === '/api/system/info') return { data: { started_at: 't0', version: 'v0.1.0' } }
    throw new Error(`unexpected GET ${url}`)
  })
})

describe('updates honesty rule (I1)', () => {
  it('derives the verdict from checked/up_to_date, never from update_available alone', () => {
    expect(updateStatus(info({ updates_enabled: false, checked: false }))).toBe('disabled')
    expect(updateStatus(info({ checked: false, reason: 'GitHub unreachable' }))).toBe('unchecked')
    expect(updateStatus(info({ up_to_date: true }))).toBe('up_to_date')
    expect(updateStatus(info({ update_available: true, latest_version: 'v0.2.0' }))).toBe('available')
    // A dev build: checked, but neither available nor up to date.
    expect(updateStatus(info({ reason: 'dev build' }))).toBe('unknown')
  })

  it('with checks off it says so and never claims up to date', async () => {
    current = info({ updates_enabled: false, checked: false, reason: 'Update checks are off', latest_version: null })
    renderPage(true)
    expect(await screen.findByText('Update checks are off')).toBeInTheDocument()
    expect(screen.queryByText(/up to date/i)).toBeNull()
    expect(screen.queryByRole('button', { name: /Check now/ })).toBeNull()
  })

  it('a failed check shows the reason, not up to date', async () => {
    current = info({ checked: false, reason: 'GitHub answered 503' })
    renderPage()
    expect(await screen.findByText("Couldn't check for updates")).toBeInTheDocument()
    expect(screen.getByText('GitHub answered 503')).toBeInTheDocument()
    expect(screen.queryByText(/up to date/i)).toBeNull()
  })

  it('a dev build has no verdict', async () => {
    current = info({ current_version: 'dev', reason: 'dev builds are not compared with releases', latest_version: 'v0.2.0' })
    renderPage()
    expect(await screen.findByText('No verdict for this build')).toBeInTheDocument()
    expect(screen.queryByText(/up to date/i)).toBeNull()
  })

  it('only a real comparable check says up to date', async () => {
    current = info({ up_to_date: true })
    renderPage()
    expect(await screen.findByText('jarvisd is up to date')).toBeInTheDocument()
  })

  it('toggles the opt-in through PUT /api/update/settings', async () => {
    current = info({ updates_enabled: false, checked: false })
    put.mockResolvedValue({ data: info({ updates_enabled: true, checked: false, reason: 'not checked yet' }) })
    renderPage()
    fireEvent.click(await screen.findByRole('switch', { name: 'Check for updates' }))
    await waitFor(() => expect(put).toHaveBeenCalledWith('/api/update/settings', { enabled: true }))
    expect(await screen.findByText("Couldn't check for updates")).toBeInTheDocument()
  })
})

describe('update apply (AD5, feature-detected)', () => {
  it('shows the command, and hides Update now once the route answers 404', async () => {
    current = info({ update_available: true, latest_version: 'v0.2.0', install_command: 'curl -fsSL https://x/install.sh | sh' })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    post.mockRejectedValue(
      Object.assign(new Error('404'), { isAxiosError: true, response: { status: 404, data: { detail: 'Not found' } } }),
    )
    renderPage(true)
    // Both the dashboard banner and the page say so.
    expect(await screen.findAllByText('jarvisd v0.2.0 is available')).toHaveLength(2)
    expect(screen.getByText('curl -fsSL https://x/install.sh | sh')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /Update now/ }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/update/apply', { version: 'v0.2.0' }))
    await waitFor(() => expect(screen.queryByRole('button', { name: /Update now/ })).toBeNull())
  })

  it('a 409 shows what to run instead', async () => {
    current = info({ update_available: true, latest_version: 'v0.2.0' })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    post.mockRejectedValue(
      Object.assign(new Error('409'), {
        isAxiosError: true,
        response: { status: 409, data: { detail: 'jarvisd is not supervised', command: 'sudo systemctl restart jarvisd' } },
      }),
    )
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /Update now/ }))
    expect(await screen.findByText('jarvisd is not supervised')).toBeInTheDocument()
    expect(screen.getByText('sudo systemctl restart jarvisd')).toBeInTheDocument()
  })
})

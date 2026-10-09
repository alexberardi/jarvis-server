import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { apiClient } from '@/api/client'
import { resetFeatures } from '@/lib/features'
import type { SystemInfo } from '@/types/system'
import PowerControls from './PowerControls'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(), loading: vi.fn() }),
}))

const post = vi.spyOn(apiClient, 'post')

function httpError(status: number, data: unknown) {
  return Object.assign(new Error(String(status)), { isAxiosError: true, response: { status, data } })
}

function info(over: Partial<SystemInfo> = {}): SystemInfo {
  return {
    hostname: 'box',
    platform: 'linux',
    release: '6',
    cpuCount: 8,
    totalMemoryMb: 16384,
    version: 'v1',
    uptime: 10,
    supervisor: 'systemd',
    restart_supported: true,
    capabilities: { restart: true, stop: true, self_update: false },
    stop: {
      supported: true,
      start_command: 'sudo jarvisd service start',
      start_note: 'Run it on the server. Restarting the computer also starts jarvisd.',
    },
    ...over,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  resetFeatures()
})

describe('PowerControls (AD8b stop)', () => {
  it('confirms, says jarvisd stays stopped, then shows the start command', async () => {
    post.mockResolvedValue({ status: 202, data: { stopping: true, start_command: 'sudo jarvisd service start', start_note: 'Run it on the server.' } })
    render(<PowerControls info={info()} />)
    expect(screen.getByRole('button', { name: /restart/i })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /^stop$/i }))

    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('Stop jarvisd?')
    expect(dialog).toHaveTextContent('this admin page stop working')
    expect(dialog).toHaveTextContent('stays stopped until you start it again')
    expect(dialog).toHaveTextContent('sudo jarvisd service start')
    expect(dialog).toHaveTextContent('Restarting the computer also starts jarvisd')
    expect(post).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: /stop jarvisd/i }))
    await waitFor(() => expect(screen.getByRole('dialog')).toHaveTextContent('jarvisd is stopping'))
    expect(post).toHaveBeenCalledWith('/api/system/stop')
    expect(screen.getByRole('dialog')).toHaveTextContent('sudo jarvisd service start')
    expect(screen.getByRole('button', { name: /copy command/i })).toBeInTheDocument()
    // The page can't come back from a stopped server: no way to "wait" or close into a dead page.
    expect(screen.queryByRole('button', { name: /close/i })).toBeNull()
  })

  it('cancel stops nothing', () => {
    render(<PowerControls info={info()} />)
    fireEvent.click(screen.getByRole('button', { name: /^stop$/i }))
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(post).not.toHaveBeenCalled()
  })

  it('is disabled with the reason when the service definition would restart jarvisd', () => {
    render(
      <PowerControls
        info={info({
          capabilities: { restart: true, stop: false },
          stop: {
            supported: false,
            reason: 'The installed unit was written by an older version.',
            command: 'sudo jarvisd service install',
            start_command: 'sudo jarvisd service start',
            start_note: '',
          },
        })}
      />,
    )
    const btn = screen.getByRole('button', { name: /^stop$/i })
    expect(btn).toBeDisabled()
    expect(btn).toHaveAttribute('title', expect.stringContaining('older version'))
    expect(btn).toHaveAttribute('title', expect.stringContaining('sudo jarvisd service install'))
  })

  it('shows the refusal when the server answers 409', async () => {
    post.mockRejectedValue(httpError(409, { detail: 'Update the unit first.', command: 'sudo jarvisd service install' }))
    render(<PowerControls info={info()} />)
    fireEvent.click(screen.getByRole('button', { name: /^stop$/i }))
    fireEvent.click(screen.getByRole('button', { name: /stop jarvisd/i }))
    await waitFor(() => expect(screen.getByRole('dialog')).toHaveTextContent('Update the unit first.'))
    expect(screen.getByRole('dialog')).toHaveTextContent('sudo jarvisd service install')
    fireEvent.click(screen.getByRole('button', { name: /close/i }))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('has no Stop button on a jarvisd without it, and no Restart unsupervised', () => {
    render(<PowerControls info={info({ capabilities: { restart: false }, restart_supported: false, stop: undefined })} />)
    expect(screen.queryByRole('button', { name: /^stop$/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /restart/i })).toBeNull()
  })
})

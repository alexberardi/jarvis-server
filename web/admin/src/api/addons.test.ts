import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from './client'
import { AgentDownloadError, fetchAgent } from './addons'
import { agentTarget } from '@/lib/agentDownload'
import { LINKS } from '@/lib/addons'

const get = vi.spyOn(apiClient, 'get')
const deb = agentTarget('linux-amd64-deb')!

function httpError(status: number, data: unknown) {
  return Object.assign(new Error(String(status)), { isAxiosError: true, response: { status, data } })
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('fetchAgent (install plan 3.1)', () => {
  it('downloads the verified file as a blob with its name and release', async () => {
    const blob = new Blob(['deb'])
    get.mockResolvedValue({
      status: 200,
      data: blob,
      headers: { 'content-disposition': 'attachment; filename="jarvis-automation-agent_1.2.0_amd64.deb"', 'x-jarvis-release': 'v1.2.0' },
    })
    const f = await fetchAgent(deb)
    expect(get).toHaveBeenCalledWith('/api/addons/agent/linux/amd64', expect.objectContaining({ responseType: 'blob' }))
    expect(f).toEqual({ blob, filename: 'jarvis-automation-agent_1.2.0_amd64.deb', release: 'v1.2.0' })
  })

  it("passes on the server's refusal and its release page", async () => {
    const body = new Blob([JSON.stringify({ detail: 'SHA256SUMS: signature check failed', release_url: 'https://example/releases/tag/v1.2.0' })])
    get.mockRejectedValue(httpError(502, body))
    const err = await fetchAgent(deb).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(AgentDownloadError)
    expect(err).toMatchObject({ status: 502, message: 'SHA256SUMS: signature check failed', releaseUrl: 'https://example/releases/tag/v1.2.0' })
  })

  it('falls back to the release page on a server without the route', async () => {
    get.mockRejectedValue(httpError(404, new Blob([JSON.stringify({ detail: 'Not Found' })])))
    const err = await fetchAgent(deb).catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 404, releaseUrl: LINKS.releases })
    expect((err as Error).message).toMatch(/can't hand out the agent/)
  })

  it('falls back to the release page when the server is unreachable', async () => {
    get.mockRejectedValue(new Error('Network Error'))
    const err = await fetchAgent(deb).catch((e: unknown) => e)
    expect(err).toMatchObject({ status: undefined, releaseUrl: LINKS.releases })
  })
})

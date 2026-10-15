import { isAxiosError } from 'axios'
import { apiClient } from './client'
import { agentDownloadPath, filenameFromDisposition, type AgentTarget } from '@/lib/agentDownload'
import { LINKS } from '@/lib/addons'

/** AgentDownloadError says why the verified download failed, and where the release page is. */
export class AgentDownloadError extends Error {
  status: number | undefined
  releaseUrl: string
  constructor(message: string, status: number | undefined, releaseUrl: string) {
    super(message)
    this.name = 'AgentDownloadError'
    this.status = status
    this.releaseUrl = releaseUrl
  }
}

export interface AgentFile {
  blob: Blob
  filename: string
  release: string | null
}

function readText(data: unknown): Promise<string> | null {
  if (typeof data === 'string') return Promise.resolve(data)
  // A Blob (responseType 'blob'). Not every Blob has text() (jsdom's doesn't): FileReader then.
  const text = (data as { text?: () => Promise<string> } | null)?.text
  if (typeof text === 'function') return text.call(data)
  if (typeof Blob !== 'undefined' && data instanceof Blob && typeof FileReader !== 'undefined') {
    return new Promise((resolve, reject) => {
      const r = new FileReader()
      r.onload = () => resolve(String(r.result ?? ''))
      r.onerror = () => reject(r.error)
      r.readAsText(data)
    })
  }
  return null
}

async function bodyOf(data: unknown): Promise<Record<string, unknown> | null> {
  try {
    const text = readText(data)
    if (text) return JSON.parse(await text) as Record<string, unknown>
    return data && typeof data === 'object' ? (data as Record<string, unknown>) : null
  } catch {
    return null
  }
}

/**
 * fetchAgent downloads the desktop agent through jarvisd, which only sends a file that passed
 * the release signature check (install plan 3.1). It goes through axios rather than a plain
 * link so the request carries the session's token (and refreshes it). A server without the
 * route (404 with no release link), a dev build (409), a refused or unreachable release (502)
 * all become an AgentDownloadError with the release page to fall back to.
 */
export async function fetchAgent(t: AgentTarget, onProgress?: (done: number, total: number | undefined) => void): Promise<AgentFile> {
  try {
    const res = await apiClient.get<Blob>(agentDownloadPath(t), {
      responseType: 'blob',
      onDownloadProgress: (e) => onProgress?.(e.loaded, e.total),
    })
    const h = res.headers as Record<string, string | undefined>
    return {
      blob: res.data,
      filename: filenameFromDisposition(h['content-disposition'], `jarvis-agent-${t.id}`),
      release: h['x-jarvis-release'] ?? null,
    }
  } catch (err) {
    if (isAxiosError(err) && err.response) {
      const body = await bodyOf(err.response.data)
      const detail = typeof body?.detail === 'string' && body.detail ? body.detail : null
      const url = typeof body?.release_url === 'string' && body.release_url ? body.release_url : LINKS.releases
      const status = err.response.status
      // The BFF's own refusals carry release_url; anything else (an older server without the
      // route, a proxy's error page) gets the plain fallback.
      const msg = detail && body?.release_url ? detail : "This server can't hand out the agent. Get it from the release page."
      throw new AgentDownloadError(msg, status, url)
    }
    throw new AgentDownloadError(
      "Couldn't reach this server to download the agent. Get it from the release page.",
      undefined,
      LINKS.releases,
    )
  }
}

/** saveFile hands a downloaded file to the browser's Save flow. */
export function saveFile(file: AgentFile): void {
  const url = URL.createObjectURL(file.blob)
  const a = document.createElement('a')
  a.href = url
  a.download = file.filename
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
  // The click has started the save; give the browser a moment before freeing the blob.
  setTimeout(() => URL.revokeObjectURL(url), 60_000)
}

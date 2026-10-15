/**
 * The desktop agent builds the server can hand out (install plan 3.1:
 * GET /api/addons/agent/{os}/{arch}): jarvisd fetches the one matching its own version from the
 * release, verifies it against the signed SHA256SUMS and streams it. These are the builds a
 * release carries (release.yml); the server answers 404 for anything else.
 */

export interface AgentTarget {
  id: string
  os: 'linux' | 'darwin' | 'windows'
  arch: 'amd64' | 'arm64'
  /** 'archive' asks for the tarball on Linux instead of the .deb. */
  format?: 'archive'
  /** The picker's label. */
  label: string
  /** The download button's short name ("Download for …"). */
  short: string
  /** What to do with the file once it is saved. */
  hint: string
}

export const AGENT_TARGETS: AgentTarget[] = [
  {
    id: 'linux-amd64-deb',
    os: 'linux',
    arch: 'amd64',
    label: 'Linux: Ubuntu, Debian (x64, .deb)',
    short: 'Linux (.deb)',
    hint: 'Open the downloaded file to install it with App Center or Software, then open Jarvis Automation Agent from your apps.',
  },
  {
    id: 'linux-arm64-deb',
    os: 'linux',
    arch: 'arm64',
    label: 'Linux: Ubuntu, Debian (ARM64, .deb)',
    short: 'Linux ARM (.deb)',
    hint: 'Open the downloaded file to install it with App Center or Software, then open Jarvis Automation Agent from your apps.',
  },
  {
    id: 'linux-amd64-archive',
    os: 'linux',
    arch: 'amd64',
    format: 'archive',
    label: 'Linux: other (x64, .tar.gz)',
    short: 'Linux (.tar.gz)',
    hint: 'Unpack it and open jarvis-agent. It sets itself up for your account the first time.',
  },
  {
    id: 'linux-arm64-archive',
    os: 'linux',
    arch: 'arm64',
    format: 'archive',
    label: 'Linux: other (ARM64, .tar.gz)',
    short: 'Linux ARM (.tar.gz)',
    hint: 'Unpack it and open jarvis-agent. It sets itself up for your account the first time.',
  },
  {
    id: 'darwin-arm64',
    os: 'darwin',
    arch: 'arm64',
    label: 'macOS (Apple silicon)',
    short: 'macOS',
    hint: 'Unpack it and open jarvis-agent. Its own page then walks you through the Mac permissions.',
  },
  {
    id: 'windows-amd64',
    os: 'windows',
    arch: 'amd64',
    label: 'Windows (x64)',
    short: 'Windows',
    hint: 'Unpack the zip and open jarvis-agent.exe.',
  },
]

export function agentTarget(id: string): AgentTarget | undefined {
  return AGENT_TARGETS.find((t) => t.id === id)
}

/** agentDownloadPath is the BFF route for a target. */
export function agentDownloadPath(t: AgentTarget): string {
  return `/api/addons/agent/${t.os}/${t.arch}${t.format ? `?format=${t.format}` : ''}`
}

/**
 * detectAgentTarget guesses the build for this browser's computer from its user agent (and,
 * in Chromium, the high-entropy architecture hint): the .deb on Linux, Apple silicon on a Mac
 * (the only Mac build), x64 on Windows. null when the page can't tell (a phone, an unknown OS):
 * the picker then has nothing preselected.
 */
export function detectAgentTarget(ua: string, platform = '', arch = ''): AgentTarget | null {
  const s = `${platform} ${ua}`.toLowerCase()
  if (/iphone|ipad|android/.test(s)) return null
  const arm = arch ? arch.toLowerCase().startsWith('arm') : /aarch64|arm64|armv8/.test(s)
  if (s.includes('mac')) return agentTarget('darwin-arm64') ?? null
  if (s.includes('win')) return agentTarget('windows-amd64') ?? null
  if (s.includes('linux') || s.includes('x11')) return agentTarget(arm ? 'linux-arm64-deb' : 'linux-amd64-deb') ?? null
  return null
}

/** filenameFromDisposition reads `attachment; filename="…"`; fallback when it is missing. */
export function filenameFromDisposition(header: string | undefined | null, fallback: string): string {
  const m = /filename="?([^";]+)"?/i.exec(header ?? '')
  const name = m?.[1]?.trim()
  // Never a path: the browser would strip it anyway, but keep only the last segment.
  return name ? name.split(/[\\/]/).pop() || fallback : fallback
}

export type BrowserKind = 'firefox' | 'chrome' | 'other'

/** detectBrowser picks which extension store fits this browser (Edge, Brave, Opera use Chrome's). */
export function detectBrowser(ua: string): BrowserKind {
  const s = ua.toLowerCase()
  if (s.includes('firefox/')) return 'firefox'
  if (s.includes('chrome/') || s.includes('chromium/') || s.includes('edg/')) return 'chrome'
  return 'other'
}

import { describe, expect, it } from 'vitest'
import { AGENT_TARGETS, agentDownloadPath, agentTarget, detectAgentTarget, detectBrowser, filenameFromDisposition } from './agentDownload'

const FF_LINUX = 'Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0'
const CHROME_ARM_LINUX = 'Mozilla/5.0 (X11; Linux aarch64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36'
const SAFARI_MAC = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15'
const EDGE_WIN = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36 Edg/129.0'
const IPHONE = 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148'

describe('agent download targets (install plan 3.1)', () => {
  it('maps each target onto the BFF route', () => {
    expect(agentDownloadPath(agentTarget('linux-amd64-deb')!)).toBe('/api/addons/agent/linux/amd64')
    expect(agentDownloadPath(agentTarget('linux-arm64-archive')!)).toBe('/api/addons/agent/linux/arm64?format=archive')
    expect(agentDownloadPath(agentTarget('darwin-arm64')!)).toBe('/api/addons/agent/darwin/arm64')
    expect(agentDownloadPath(agentTarget('windows-amd64')!)).toBe('/api/addons/agent/windows/amd64')
    // Only the builds a release carries (release.yml).
    expect(new Set(AGENT_TARGETS.map((t) => `${t.os}-${t.arch}`))).toEqual(
      new Set(['linux-amd64', 'linux-arm64', 'darwin-arm64', 'windows-amd64']),
    )
  })

  it("detects this computer's build", () => {
    expect(detectAgentTarget(FF_LINUX)?.id).toBe('linux-amd64-deb')
    expect(detectAgentTarget(CHROME_ARM_LINUX)?.id).toBe('linux-arm64-deb')
    // Chromium's client hint wins over the user agent string.
    expect(detectAgentTarget(FF_LINUX, '', 'arm')?.id).toBe('linux-arm64-deb')
    expect(detectAgentTarget(CHROME_ARM_LINUX, '', 'x86')?.id).toBe('linux-amd64-deb')
    // Every Mac browser says Intel; the only Mac build is Apple silicon.
    expect(detectAgentTarget(SAFARI_MAC, 'MacIntel')?.id).toBe('darwin-arm64')
    expect(detectAgentTarget(EDGE_WIN, 'Win32')?.id).toBe('windows-amd64')
    expect(detectAgentTarget(IPHONE, 'iPhone')).toBeNull()
    expect(detectAgentTarget('curl/8')).toBeNull()
  })

  it('reads the file name from Content-Disposition, never a path', () => {
    expect(filenameFromDisposition('attachment; filename="jarvis-automation-agent_1.2.0_amd64.deb"', 'x')).toBe(
      'jarvis-automation-agent_1.2.0_amd64.deb',
    )
    expect(filenameFromDisposition('attachment; filename="../../etc/passwd"', 'x')).toBe('passwd')
    expect(filenameFromDisposition(undefined, 'fallback')).toBe('fallback')
  })

  it('picks the extension store for this browser', () => {
    expect(detectBrowser(FF_LINUX)).toBe('firefox')
    expect(detectBrowser(CHROME_ARM_LINUX)).toBe('chrome')
    expect(detectBrowser(EDGE_WIN)).toBe('chrome')
    expect(detectBrowser(SAFARI_MAC)).toBe('other')
  })
})

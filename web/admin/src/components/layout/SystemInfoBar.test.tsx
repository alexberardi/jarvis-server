import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import SystemInfoBar from './SystemInfoBar'

const info = vi.hoisted(() => ({ version: 'v0.0.0-rehearsal' }))
vi.mock('@/hooks/useSystem', () => ({
  useSystemInfo: () => ({
    data: {
      version: info.version,
      uptime: 60,
      platform: 'linux',
      arch: 'amd64',
      hostname: 'box',
      cpuCount: 4,
      totalMemoryMb: 8192,
      listeners: [],
    },
  }),
}))

describe('SystemInfoBar version', () => {
  // A10 rehearsal: release tags already start with "v"; the footer said "jarvisd vv0.0.0-rehearsal".
  it('shows a release tag as is', () => {
    info.version = 'v0.0.0-rehearsal'
    render(<SystemInfoBar />)
    expect(screen.getByText(/jarvisd v0\.0\.0-rehearsal/)).toBeInTheDocument()
    expect(screen.queryByText(/vv0/)).toBeNull()
  })

  it('prefixes a bare number and leaves a dev build alone', () => {
    info.version = '1.2.3'
    const { unmount } = render(<SystemInfoBar />)
    expect(screen.getByText(/jarvisd v1\.2\.3/)).toBeInTheDocument()
    unmount()
    info.version = 'dev'
    render(<SystemInfoBar />)
    expect(screen.getByText(/jarvisd dev/)).toBeInTheDocument()
  })
})

/**
 * The Hardware screen must name the platform it was given.
 *
 * This is the screen a user reported twice. First it told a Windows user they
 * were on **macOS** (Docker Desktop identifies itself identically on both, and
 * the server trusted that over its own process.platform). Once that was fixed
 * it told them they were on **Linux**, because the value was being collapsed to
 * darwin|linux on its way to the client.
 *
 * Neither was caught, because this repo had no frontend test runner at all --
 * the label was never executed by anything. A server-side source guard covered
 * it in the meantime; this replaces that proxy with the real thing.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import HardwareStep from './HardwareStep'
import { WizardProvider } from '@/context/WizardContext'
import type { HardwareInfo } from '@/types/wizard'

const getHardwareInfo = vi.hoisted(() => vi.fn())
vi.mock('@/api/install', () => ({ getHardwareInfo }))

function hardware(over: Partial<HardwareInfo> = {}): HardwareInfo {
  return {
    platform: 'linux',
    arch: 'x64',
    totalMemoryGb: 32,
    gpuName: null,
    gpuVramMb: null,
    gpuType: 'none',
    recommendedBackends: ['gguf'],
    recommendedBackend: 'gguf',
    ...over,
  } as HardwareInfo
}

const renderStep = () =>
  render(
    <WizardProvider>
      <HardwareStep />
    </WizardProvider>,
  )

beforeEach(() => {
  getHardwareInfo.mockReset()
})

describe('the platform label', () => {
  it('says Windows for win32 — the regression', async () => {
    getHardwareInfo.mockResolvedValue(hardware({ platform: 'win32', arch: 'x64' }))

    renderStep()

    expect(await screen.findByText('Windows (x64)')).toBeInTheDocument()
    expect(screen.queryByText(/Linux \(x64\)/)).not.toBeInTheDocument()
    expect(screen.queryByText(/macOS/)).not.toBeInTheDocument()
  })

  it('says macOS for darwin', async () => {
    getHardwareInfo.mockResolvedValue(hardware({ platform: 'darwin', arch: 'arm64' }))

    renderStep()

    expect(await screen.findByText('macOS (arm64)')).toBeInTheDocument()
  })

  it('says Linux for linux', async () => {
    getHardwareInfo.mockResolvedValue(hardware({ platform: 'linux', arch: 'x64' }))

    renderStep()

    expect(await screen.findByText('Linux (x64)')).toBeInTheDocument()
  })

  it('shows the raw value rather than a wrong name for something unexpected', async () => {
    // Failing visibly beats confidently mislabelling, which is the whole
    // lesson of this screen.
    getHardwareInfo.mockResolvedValue(hardware({ platform: 'freebsd' as never }))

    renderStep()

    expect(await screen.findByText('freebsd (x64)')).toBeInTheDocument()
  })
})

describe('the GPU line', () => {
  it('names a detected card', async () => {
    getHardwareInfo.mockResolvedValue(
      hardware({
        platform: 'win32',
        gpuName: 'NVIDIA GeForce RTX 3050 Laptop GPU',
        gpuVramMb: 4096,
        gpuType: 'nvidia',
      }),
    )

    renderStep()

    expect(await screen.findByText(/RTX 3050/)).toBeInTheDocument()
  })

  it('says so plainly when there is none', async () => {
    getHardwareInfo.mockResolvedValue(hardware({ platform: 'win32' }))

    renderStep()

    expect(await screen.findByText(/No GPU detected/i)).toBeInTheDocument()
  })
})

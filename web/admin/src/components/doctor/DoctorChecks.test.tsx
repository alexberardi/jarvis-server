import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import DoctorChecks from './DoctorChecks'

const FIX = 'sudo ufw allow from 10.0.0.0/24 to any port 17700,17702,17031 proto tcp comment jarvisd'

describe('DoctorChecks fix box', () => {
  it('shows the fix whole, wrapping only at spaces and after commas', () => {
    render(<DoctorChecks checks={[{ name: 'firewall 10.0.0.0/24', status: 'fail', detail: 'blocked', fix: FIX }]} />)
    const pre = screen.getByText((_, el) => el?.tagName === 'PRE' && el.textContent === FIX)
    // Never mid-token: no break-all, and long tokens scroll inside the box instead.
    expect(pre.className).not.toMatch(/break-all/)
    expect(pre.className).toMatch(/break-normal/)
    expect(pre.className).toMatch(/overflow-x-auto/)
    // A port list is one long token; it may break after each comma, so ports stay whole.
    expect(pre.querySelectorAll('wbr')).toHaveLength(2)
  })

  it('copies the exact command', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    render(<DoctorChecks checks={[{ name: 'firewall', status: 'warn', detail: '', fix: FIX }]} />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy fix' }))
    expect(writeText).toHaveBeenCalledWith(FIX)
  })

  it('hides the fix when the check passed', () => {
    render(<DoctorChecks checks={[{ name: 'firewall', status: 'ok', detail: '', fix: FIX }]} />)
    expect(screen.queryByRole('button', { name: 'Copy fix' })).not.toBeInTheDocument()
  })
})

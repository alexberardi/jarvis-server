import { describe, expect, it } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import { toast } from 'sonner'
import { ThemeProvider } from '@/theme/ThemeProvider'
import Toaster from './Toast'

function renderToaster(theme: 'light' | 'dark') {
  localStorage.setItem('jarvis-admin-theme', theme)
  return render(
    <ThemeProvider>
      <Toaster />
    </ThemeProvider>,
  )
}

describe('Toaster', () => {
  it('shows a toast sent with sonner', async () => {
    renderToaster('light')
    act(() => {
      toast.success('Settings saved')
    })
    expect(await screen.findByText('Settings saved')).toBeInTheDocument()
  })

  it('follows the app theme and draws with the tokens', async () => {
    const { container } = renderToaster('dark')
    act(() => {
      toast('Hello')
    })
    await screen.findByText('Hello')
    const toaster = container.ownerDocument.querySelector<HTMLElement>('[data-sonner-toaster]')!
    expect(toaster).toHaveAttribute('data-sonner-theme', 'dark')
    expect(toaster.style.getPropertyValue('--normal-bg')).toBe('var(--color-surface)')
    expect(toaster.style.getPropertyValue('--error-text')).toBe('var(--color-danger)')
  })
})

import type { CSSProperties } from 'react'
import { Toaster as Sonner } from 'sonner'
import { useTheme } from '@/theme/ThemeProvider'

// Sonner's colour variables, pointed at B's tokens: the surface for a plain toast, each
// status's soft background and colour (AA-checked in tokens.test.ts) for richColors.
const style = {
  '--normal-bg': 'var(--color-surface)',
  '--normal-border': 'var(--color-border)',
  '--normal-text': 'var(--color-text)',
  '--success-bg': 'var(--color-success-soft)',
  '--success-border': 'var(--color-success-soft)',
  '--success-text': 'var(--color-success)',
  '--warning-bg': 'var(--color-warning-soft)',
  '--warning-border': 'var(--color-warning-soft)',
  '--warning-text': 'var(--color-warning)',
  '--error-bg': 'var(--color-danger-soft)',
  '--error-border': 'var(--color-danger-soft)',
  '--error-text': 'var(--color-danger)',
  '--info-bg': 'var(--color-primary-soft)',
  '--info-border': 'var(--color-primary-soft)',
  '--info-text': 'var(--color-text)',
  '--border-radius': 'var(--radius-card)',
  fontFamily: 'var(--font-sans)',
} as CSSProperties

/**
 * Toaster is the app's one Sonner toaster, in B's colours and the app's theme. Toasts come up
 * from the bottom in 200ms and leave in 160ms (index.css); call sonner's `toast()` to show one.
 */
export default function Toaster() {
  const { theme } = useTheme()
  return <Sonner theme={theme} position="bottom-right" richColors closeButton={false} style={style} />
}

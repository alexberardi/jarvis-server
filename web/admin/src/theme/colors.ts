/**
 * The colour tokens for JS (direction B, Quiet Violet): the same values as src/index.css and
 * docs/design/directions/tokens.py. tokens.test.ts checks the three agree and that every pair
 * the UI draws passes WCAG AA in both themes.
 */
export const colors = {
  light: {
    background: '#f6f6f8',
    surface: '#ffffff',
    'surface-alt': '#f0f0f3',
    border: '#e2e2e8',
    'border-strong': '#84848f',
    text: '#16161b',
    'text-muted': '#5b5b68',
    primary: '#8A00C4',
    'on-primary': '#ffffff',
    'primary-soft': '#f2e6fa',
    success: '#1d7339',
    'success-soft': '#e3f3e8',
    warning: '#8a5300',
    'warning-soft': '#fbefd9',
    danger: '#bf2530',
    'danger-soft': '#fbe5e6',
    focus: '#8A00C4',
  },
  dark: {
    background: '#111115',
    surface: '#1a1a20',
    'surface-alt': '#23232b',
    border: '#2e2e38',
    'border-strong': '#787889',
    text: '#f2f2f5',
    'text-muted': '#a6a6b4',
    primary: '#c084fc',
    'on-primary': '#1d0b2b',
    'primary-soft': '#2e1f3d',
    success: '#72d697',
    'success-soft': '#173022',
    warning: '#f0b44c',
    'warning-soft': '#352a16',
    danger: '#ff8f8f',
    'danger-soft': '#3a1d20',
    focus: '#c084fc',
  },
} as const

export type ColorToken = keyof typeof colors.light

/** contrast is the WCAG 2 contrast ratio of two #rrggbb colours. */
export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

function luminance(hex: string): number {
  const h = hex.replace('#', '')
  const [r, g, b] = [0, 2, 4].map((i) => {
    const c = parseInt(h.slice(i, i + 2), 16) / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

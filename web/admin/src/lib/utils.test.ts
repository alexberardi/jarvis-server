import { describe, expect, it } from 'vitest'
import { cn } from './utils'

describe('cn', () => {
  it('keeps a token text size next to a token colour', () => {
    expect(cn('text-body', 'text-text-muted')).toBe('text-body text-text-muted')
  })

  it('lets a later token win over an earlier one of the same kind', () => {
    expect(cn('text-small', 'text-body')).toBe('text-body')
    expect(cn('rounded-card', 'rounded-control')).toBe('rounded-control')
    expect(cn('bg-surface', 'bg-primary')).toBe('bg-primary')
  })
})

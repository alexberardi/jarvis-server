import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import Button from './Button'

describe('Button', () => {
  it('is a type=button that calls onClick', () => {
    const onClick = vi.fn()
    render(<Button onClick={onClick}>Save</Button>)
    const b = screen.getByRole('button', { name: 'Save' })
    expect(b).toHaveAttribute('type', 'button')
    fireEvent.click(b)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('does nothing when disabled', () => {
    const onClick = vi.fn()
    render(
      <Button disabled onClick={onClick}>
        Save
      </Button>,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(onClick).not.toHaveBeenCalled()
  })

  it('is busy and blocks clicks while loading, keeping its label', () => {
    const onClick = vi.fn()
    render(
      <Button loading onClick={onClick}>
        Save
      </Button>,
    )
    const b = screen.getByRole('button', { name: 'Save' })
    expect(b).toBeDisabled()
    expect(b).toHaveAttribute('aria-busy', 'true')
    fireEvent.click(b)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('draws the variant from the tokens and has the press feedback', () => {
    render(
      <>
        <Button variant="primary">A</Button>
        <Button variant="danger">B</Button>
      </>,
    )
    expect(screen.getByRole('button', { name: 'A' })).toHaveClass('bg-primary', 'text-on-primary', 'press')
    expect(screen.getByRole('button', { name: 'B' })).toHaveClass('bg-danger')
  })

  it('submits a form when asked to', () => {
    const onSubmit = vi.fn((e: { preventDefault: () => void }) => e.preventDefault())
    render(
      <form onSubmit={onSubmit}>
        <Button type="submit">Go</Button>
      </form>,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Go' }))
    expect(onSubmit).toHaveBeenCalled()
  })
})

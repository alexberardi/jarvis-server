import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import Dialog, { Sheet } from './Dialog'

function Harness({ sheet = false, withField = true }: { sheet?: boolean; withField?: boolean }) {
  const [open, setOpen] = useState(false)
  const D = sheet ? Sheet : Dialog
  return (
    <>
      <button onClick={() => setOpen(true)}>Rename</button>
      <D
        open={open}
        onClose={() => setOpen(false)}
        title="Rename node"
        description="The name the household sees"
        footer={<button onClick={() => setOpen(false)}>Save</button>}
      >
        {withField && <input aria-label="Name" />}
      </D>
    </>
  )
}

const opener = () => screen.getByRole('button', { name: 'Rename' })
const dialog = () => screen.queryByRole('dialog', { name: 'Rename node' })

describe('Dialog', () => {
  it('renders nothing until opened, then a labelled, described modal', () => {
    render(<Harness />)
    expect(dialog()).toBeNull()
    fireEvent.click(opener())
    const d = dialog()!
    expect(d).toHaveAttribute('aria-modal', 'true')
    expect(d).toHaveAccessibleDescription('The name the household sees')
  })

  it('moves focus to the first field in its body', () => {
    render(<Harness />)
    fireEvent.click(opener())
    expect(screen.getByRole('textbox', { name: 'Name' })).toHaveFocus()
  })

  it('focuses itself when the body has nothing to focus', () => {
    render(<Harness withField={false} />)
    fireEvent.click(opener())
    expect(dialog()).toHaveFocus()
  })

  it('closes on Escape and gives focus back to the opener', async () => {
    render(<Harness />)
    opener().focus()
    fireEvent.click(opener())
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Name' }), { key: 'Escape' })
    expect(opener()).toHaveFocus()
    expect(dialog()).toHaveAttribute('data-state', 'closed')
    await waitFor(() => expect(dialog()).toBeNull())
  })

  it('closes from the backdrop and the close button', async () => {
    render(<Harness />)
    fireEvent.click(opener())
    fireEvent.click(screen.getByTestId('dialog-backdrop'))
    await waitFor(() => expect(dialog()).toBeNull())
    fireEvent.click(opener())
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(dialog()).toBeNull())
  })

  it('keeps Tab inside: from the last control to the first and back', () => {
    render(<Harness />)
    fireEvent.click(opener())
    const close = screen.getByRole('button', { name: 'Close' })
    const save = screen.getByRole('button', { name: 'Save' })
    save.focus()
    fireEvent.keyDown(save, { key: 'Tab' })
    expect(close).toHaveFocus()
    fireEvent.keyDown(close, { key: 'Tab', shiftKey: true })
    expect(save).toHaveFocus()
  })

  it('locks the page scroll while open', async () => {
    render(<Harness />)
    fireEvent.click(opener())
    expect(document.body.style.overflow).toBe('hidden')
    fireEvent.keyDown(dialog()!, { key: 'Escape' })
    expect(document.body.style.overflow).toBe('')
  })
})

describe('Dialog that must not be dismissed', () => {
  it('has no close button, and ignores Escape and the backdrop', () => {
    const onClose = vi.fn()
    render(
      <Dialog open dismissible={false} onClose={onClose} title="Stopping">
        <p>Wait</p>
      </Dialog>,
    )
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull()
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    fireEvent.click(screen.getByTestId('dialog-backdrop'))
    expect(onClose).not.toHaveBeenCalled()
  })
})

describe('Sheet', () => {
  it('is the same modal, sliding up from the bottom on a phone', () => {
    render(<Harness sheet />)
    fireEvent.click(opener())
    expect(dialog()).toHaveClass('animate-sheet-in', 'md:animate-pop-in', 'rounded-t-card')
  })

  it('stays open on Escape and the backdrop when it must be acknowledged, and shows no close button', () => {
    function Must() {
      const [open, setOpen] = useState(true)
      return (
        <Dialog open={open} onClose={() => setOpen(false)} dismissible={false} title="Key for app" footer={<button onClick={() => setOpen(false)}>Done</button>}>
          <p>secret</p>
        </Dialog>
      )
    }
    render(<Must />)
    const d = screen.getByRole('dialog', { name: 'Key for app' })
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull()
    fireEvent.keyDown(d, { key: 'Escape' })
    fireEvent.click(screen.getByTestId('dialog-backdrop'))
    expect(screen.getByRole('dialog', { name: 'Key for app' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    return waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })
})


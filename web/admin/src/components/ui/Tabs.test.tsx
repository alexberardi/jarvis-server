import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import Tabs from './Tabs'

const tabs = [
  { id: 'general', label: 'General', content: <p>general panel</p> },
  { id: 'voice', label: 'Voice', content: <p>voice panel</p> },
  { id: 'beta', label: 'Beta', content: <p>beta panel</p>, disabled: true },
  { id: 'advanced', label: 'Advanced', content: <p>advanced panel</p> },
]

const tab = (name: string) => screen.getByRole('tab', { name })

describe('Tabs', () => {
  it('selects the first tab and shows its panel, labelled by it', () => {
    render(<Tabs label="Settings" tabs={tabs} />)
    expect(screen.getByRole('tablist', { name: 'Settings' })).toBeInTheDocument()
    expect(tab('General')).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tabpanel', { name: 'General' })).toHaveTextContent('general panel')
  })

  it('has one tab stop: only the selected tab is tabbable', () => {
    render(<Tabs label="Settings" tabs={tabs} defaultValue="voice" />)
    expect(tab('Voice')).toHaveAttribute('tabindex', '0')
    expect(tab('General')).toHaveAttribute('tabindex', '-1')
  })

  it('moves and selects with the arrow keys, skipping disabled tabs and wrapping', () => {
    render(<Tabs label="Settings" tabs={tabs} />)
    tab('General').focus()
    fireEvent.keyDown(tab('General'), { key: 'ArrowRight' })
    expect(tab('Voice')).toHaveFocus()
    expect(tab('Voice')).toHaveAttribute('aria-selected', 'true')
    fireEvent.keyDown(tab('Voice'), { key: 'ArrowRight' })
    expect(tab('Advanced')).toHaveFocus()
    fireEvent.keyDown(tab('Advanced'), { key: 'ArrowRight' })
    expect(tab('General')).toHaveFocus()
    fireEvent.keyDown(tab('General'), { key: 'ArrowLeft' })
    expect(tab('Advanced')).toHaveFocus()
    expect(screen.getByRole('tabpanel')).toHaveTextContent('advanced panel')
  })

  it('jumps with Home and End', () => {
    render(<Tabs label="Settings" tabs={tabs} defaultValue="voice" />)
    fireEvent.keyDown(tab('Voice'), { key: 'End' })
    expect(tab('Advanced')).toHaveFocus()
    fireEvent.keyDown(tab('Advanced'), { key: 'Home' })
    expect(tab('General')).toHaveFocus()
  })

  it('selects on click and reports it; a controlled value wins', () => {
    const onValueChange = vi.fn()
    render(<Tabs label="Settings" tabs={tabs} value="general" onValueChange={onValueChange} />)
    fireEvent.click(tab('Voice'))
    expect(onValueChange).toHaveBeenCalledWith('voice')
    expect(tab('General')).toHaveAttribute('aria-selected', 'true')
  })
})

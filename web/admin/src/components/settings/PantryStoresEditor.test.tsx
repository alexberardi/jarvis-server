import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import PantryStoresEditor from './PantryStoresEditor'

const stores = [
  { id: 'a1', url: 'https://pantry-api.jarvisautomation.io', name: 'Public Pantry' },
  { id: 'b2', url: 'http://10.0.0.5:7721', name: 'Workshop' },
]

function setup(value: unknown = stores) {
  const onSave = vi.fn()
  render(<PantryStoresEditor value={value} onSave={onSave} onCancel={() => {}} isSaving={false} />)
  return onSave
}

describe('PantryStoresEditor', () => {
  it('reorders and edits stores, keeping their ids, and saves a new store without one', () => {
    const onSave = setup()
    const list = screen.getByRole('list', { name: 'Pantry stores' })
    expect(within(list).getAllByRole('listitem')[0]).toHaveTextContent('Default')

    fireEvent.click(screen.getByRole('button', { name: 'Move Workshop up' }))
    fireEvent.change(screen.getAllByLabelText('URL')[0], { target: { value: 'http://10.0.0.6:7721' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add store' }))
    const names = screen.getAllByRole('textbox', { name: /Name/ })
    expect(names[2]).toHaveFocus()
    fireEvent.change(names[2], { target: { value: '  Lab  ' } })
    fireEvent.change(screen.getAllByLabelText('URL')[2], { target: { value: 'https://lab.example' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(onSave).toHaveBeenCalledWith([
      { id: 'b2', name: 'Workshop', url: 'http://10.0.0.6:7721' },
      { id: 'a1', name: 'Public Pantry', url: 'https://pantry-api.jarvisautomation.io' },
      { name: 'Lab', url: 'https://lab.example' },
    ])
  })

  it("doesn't save a store with no name or a bad URL", () => {
    const onSave = setup([])
    expect(screen.getByText(/No stores\./)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add store' }))
    fireEvent.change(screen.getByLabelText('URL'), { target: { value: 'localhost:7721' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).not.toHaveBeenCalled()
    expect(screen.getByText('Enter a name')).toBeInTheDocument()
    expect(screen.getByText('Use an http or https URL')).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('URL'), { target: { value: 'http://u:p@host' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(screen.getByText('Leave out the user name and password')).toBeInTheDocument()
  })

  it('saves an empty list when every store is removed', () => {
    const onSave = setup()
    fireEvent.click(screen.getByRole('button', { name: 'Remove Public Pantry' }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove Workshop' }))
    expect(screen.getByRole('button', { name: 'Add store' })).toHaveFocus()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledWith([])
  })
})

import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import Input from './Input'
import Select from './Select'
import Switch from './Switch'

describe('Input', () => {
  it('is labelled and describes itself with its hint', () => {
    render(<Input label="Name" hint="As the household sees it" />)
    const input = screen.getByLabelText('Name')
    expect(input).toHaveAccessibleDescription('As the household sees it')
    expect(input).not.toHaveAttribute('aria-invalid')
  })

  it('shows the error instead of the hint and marks itself invalid', () => {
    render(<Input label="Name" hint="As the household sees it" error="Required" />)
    const input = screen.getByLabelText('Name')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(input).toHaveAccessibleDescription('Required')
    expect(screen.queryByText('As the household sees it')).toBeNull()
  })

  it('passes typing through to onChange', () => {
    const onChange = vi.fn()
    render(<Input label="Name" onChange={onChange} />)
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Avery' } })
    expect(onChange).toHaveBeenCalled()
  })

  it('keeps a given id', () => {
    render(<Input id="email" label="Email" />)
    expect(screen.getByLabelText('Email')).toHaveAttribute('id', 'email')
  })
})

describe('Select', () => {
  const options = [
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ]

  it('calls onValueChange with the picked value', () => {
    const onValueChange = vi.fn()
    render(<Select label="Theme" options={options} defaultValue="light" onValueChange={onValueChange} />)
    fireEvent.change(screen.getByLabelText('Theme'), { target: { value: 'dark' } })
    expect(onValueChange).toHaveBeenCalledWith('dark')
  })

  it('lists the options, with a disabled placeholder first', () => {
    render(<Select label="Theme" options={options} placeholder="Pick one" defaultValue="" />)
    const all = screen.getAllByRole('option')
    expect(all.map((o) => o.textContent)).toEqual(['Pick one', 'Light', 'Dark'])
    expect(all[0]).toBeDisabled()
  })

  it('marks itself invalid with an error', () => {
    render(<Select label="Theme" options={options} error="Pick a theme" />)
    expect(screen.getByLabelText('Theme')).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByLabelText('Theme')).toHaveAccessibleDescription('Pick a theme')
  })
})

describe('Select groups', () => {
  it('puts grouped options under their optgroup', () => {
    render(
      <Select
        label="Voice"
        value="b"
        onChange={() => {}}
        groups={[
          { label: 'English', options: [{ value: 'a', label: 'Amy' }] },
          { label: 'French', options: [{ value: 'b', label: 'Bea' }] },
        ]}
      />,
    )
    const select = screen.getByLabelText('Voice')
    expect(select).toHaveValue('b')
    expect(select.querySelectorAll('optgroup')).toHaveLength(2)
    expect(screen.getByRole('option', { name: 'Bea' }).parentElement).toHaveAttribute('label', 'French')
  })
})

describe('Switch', () => {
  function Controlled({ onChange }: { onChange?: (v: boolean) => void }) {
    const [on, setOn] = useState(false)
    return (
      <Switch
        label="Wake word"
        description="Listen for 'Jarvis'"
        checked={on}
        onCheckedChange={(v) => {
          setOn(v)
          onChange?.(v)
        }}
      />
    )
  }

  it('is a switch that toggles aria-checked on click', () => {
    const onChange = vi.fn()
    render(<Controlled onChange={onChange} />)
    const s = screen.getByRole('switch', { name: 'Wake word' })
    expect(s).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(s)
    expect(s).toHaveAttribute('aria-checked', 'true')
    expect(onChange).toHaveBeenLastCalledWith(true)
    fireEvent.click(s)
    expect(s).toHaveAttribute('aria-checked', 'false')
  })

  it('toggles from its label too, and is described', () => {
    render(<Controlled />)
    fireEvent.click(screen.getByText('Wake word'))
    const s = screen.getByRole('switch', { name: 'Wake word' })
    expect(s).toHaveAttribute('aria-checked', 'true')
    expect(s).toHaveAccessibleDescription("Listen for 'Jarvis'")
  })

  it('does not toggle when disabled', () => {
    const onCheckedChange = vi.fn()
    render(<Switch aria-label="Updates" checked={false} disabled onCheckedChange={onCheckedChange} />)
    fireEvent.click(screen.getByRole('switch', { name: 'Updates' }))
    expect(onCheckedChange).not.toHaveBeenCalled()
  })
})

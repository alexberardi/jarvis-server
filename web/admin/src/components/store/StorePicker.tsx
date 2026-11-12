import type { PantryStore } from '@/api/store'
import { Select } from '@/components/ui'

interface StorePickerProps {
  stores: PantryStore[]
  store: PantryStore | undefined
  pick: (id: string) => void
  /** A visible label (in a form); without one the picker is named "Pantry store" for screen readers. */
  label?: string
  hint?: string
  className?: string
}

/**
 * StorePicker chooses which of the household's Pantry stores to browse and install from. It
 * renders nothing with one store: there is nothing to pick (decision 2026-11-11-several-pantries).
 */
export default function StorePicker({ stores, store, pick, label, hint, className }: StorePickerProps) {
  if (stores.length < 2) return null
  return (
    <Select
      label={label}
      hint={hint}
      aria-label={label ? undefined : 'Pantry store'}
      wrapperClassName={className}
      value={store?.id}
      onValueChange={pick}
      options={stores.map((s, i) => ({ value: s.id, label: i === 0 ? `${s.name} (default)` : s.name }))}
    />
  )
}

import { useId } from 'react'
import { Plus, X } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { listDeviceRooms, listDevices } from '@/api/devices'
import type { RoutineStepArg } from '@/api/routines'
import { Button, Input } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import type { CommandParam } from '@/lib/routines/commands'

/** Relative date words the date parameters accept (mobile's DATE_KEYS). */
const DATE_KEYS = ['today', 'tomorrow', 'yesterday', 'this_weekend', 'next_weekend', 'this_week', 'next_week']

const isArray = (t: string) => t === 'array' || t === 'list' || t.startsWith('array') || t.endsWith('[]')

function useOptions(source: string | null | undefined): string[] | null {
  const { activeHouseholdId: hh } = useAuth()
  const [kind, domain] = (source ?? '').split(':')
  const enabled = kind === 'devices' || kind === 'entities' || kind === 'rooms'
  const q = useQuery({
    queryKey: ['routines', hh, 'options', source],
    enabled,
    staleTime: 60_000,
    queryFn: async ({ signal }) => {
      if (kind === 'rooms') return (await listDeviceRooms(hh, signal)).map((r) => r.name)
      const devices = (await listDevices(hh, signal)).filter((d) => !domain || d.domain === domain)
      return [...new Set(devices.map((d) => (kind === 'entities' ? d.entity_id : d.name)).filter(Boolean))]
    },
  })
  return enabled ? (q.data ?? null) : null
}

function parseItems(v: string): string[] {
  try {
    const a = JSON.parse(v)
    if (Array.isArray(a)) return a.length ? a.map(String) : ['']
  } catch {
    // a plain value: one item
  }
  return [v]
}

interface ArgFieldProps {
  arg: RoutineStepArg
  meta: CommandParam | null
  onChange: (u: Partial<RoutineStepArg>) => void
  onRemove: () => void
}

/**
 * ArgField is one argument of a step (mobile's ParameterArgRow). A known parameter gets its
 * name as the label and its allowed values as suggestions (a live list of devices or rooms,
 * its enum, date words); any other value can still be typed. A list parameter is one field per
 * item, stored as a JSON array. An argument the command doesn't declare is a free key and value.
 */
export default function ArgField({ arg, meta, onChange, onRemove }: ArgFieldProps) {
  const listId = useId()
  const live = useOptions(meta?.options_source)
  const remove = (
    <Button size="icon" variant="ghost" aria-label={`Remove ${arg.key || 'argument'}`} onClick={onRemove} className="mt-6 shrink-0">
      <X size={16} aria-hidden />
    </Button>
  )

  if (!meta) {
    return (
      <div className="flex items-start gap-2">
        <Input label="Name" value={arg.key} onChange={(e) => onChange({ key: e.target.value })} wrapperClassName="flex-1" />
        <Input label="Value" value={arg.value} onChange={(e) => onChange({ value: e.target.value })} wrapperClassName="flex-1" />
        {remove}
      </div>
    )
  }

  const options = live ?? (meta.enum_values?.length ? meta.enum_values : meta.type.includes('date') ? DATE_KEYS : null)
  const label = `${meta.name}${meta.required ? '' : ' (optional)'}`
  const datalist = options && (
    <datalist id={listId}>
      {options.map((o) => (
        <option key={o} value={o} />
      ))}
    </datalist>
  )

  if (isArray(meta.type)) {
    const items = parseItems(arg.value)
    // Empty items stay while editing (a just-added field); the save drops them (cleanSteps).
    const set = (next: string[]) => onChange({ value: JSON.stringify(next) })
    return (
      <fieldset className="flex items-start gap-2">
        <div className="min-w-0 flex-1 space-y-2">
          <legend className="text-small font-medium text-text">{label}</legend>
          {meta.description && <p className="text-small text-text-muted">{meta.description}</p>}
          {items.map((v, i) => (
            <div key={i} className="flex gap-2">
              <Input
                aria-label={`${meta.name} ${i + 1}`}
                value={v}
                list={options ? listId : undefined}
                onChange={(e) => set(items.map((x, j) => (j === i ? e.target.value : x)))}
                wrapperClassName="flex-1"
              />
              {items.length > 1 && (
                <Button size="icon" variant="ghost" aria-label={`Remove ${meta.name} ${i + 1}`} onClick={() => set(items.filter((_, j) => j !== i))}>
                  <X size={16} aria-hidden />
                </Button>
              )}
            </div>
          ))}
          <Button size="sm" variant="ghost" icon={<Plus size={14} aria-hidden />} onClick={() => onChange({ value: JSON.stringify([...items, '']) })}>
            Add item
          </Button>
          {datalist}
        </div>
        {remove}
      </fieldset>
    )
  }

  return (
    <div className="flex items-start gap-2">
      <Input
        label={label}
        hint={meta.description ?? undefined}
        value={arg.value}
        list={options ? listId : undefined}
        placeholder={options ? 'Pick or type a value' : undefined}
        onChange={(e) => onChange({ value: e.target.value })}
        wrapperClassName="flex-1"
      />
      {datalist}
      {remove}
    </div>
  )
}

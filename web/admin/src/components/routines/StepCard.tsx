import { useId } from 'react'
import { ArrowDown, ArrowUp, Plus, X } from 'lucide-react'
import type { RoutineStep, RoutineStepArg } from '@/api/routines'
import { Button, Input, Select } from '@/components/ui'
import type { CommandMeta } from '@/lib/routines/commands'
import ArgField from './ArgField'

interface StepCardProps {
  index: number
  count: number
  step: RoutineStep
  catalog: Record<string, CommandMeta>
  onChange: (s: RoutineStep) => void
  onCommand: (command: string) => void
  onMove: (by: -1 | 1) => void
  onRemove: () => void
}

/**
 * StepCard is one step of an Advanced routine: its command (from the chosen node's catalog, or
 * typed when the catalog is unavailable), a label, and its arguments. Up/Down reorder it
 * (mobile drags; buttons work with a keyboard).
 */
export default function StepCard({ index, count, step, catalog, onChange, onCommand, onMove, onRemove }: StepCardProps) {
  const headId = useId()
  const meta = catalog[step.command]
  const names = Object.keys(catalog).sort()
  const unused = meta?.parameters.filter((p) => !step.args.some((a) => a.key === p.name)) ?? []
  const setArg = (i: number, u: Partial<RoutineStepArg>) => onChange({ ...step, args: step.args.map((a, j) => (j === i ? { ...a, ...u } : a)) })

  return (
    <section aria-labelledby={headId} className="space-y-3 rounded-control border border-border bg-surface-alt/40 p-3">
      <div className="flex items-center gap-1">
        <h3 id={headId} className="flex-1 text-small font-semibold text-primary">
          Step {index + 1}
        </h3>
        <Button size="icon" variant="ghost" aria-label={`Move step ${index + 1} up`} disabled={index === 0} onClick={() => onMove(-1)}>
          <ArrowUp size={16} aria-hidden />
        </Button>
        <Button size="icon" variant="ghost" aria-label={`Move step ${index + 1} down`} disabled={index === count - 1} onClick={() => onMove(1)}>
          <ArrowDown size={16} aria-hidden />
        </Button>
        <Button size="icon" variant="ghost" aria-label={`Remove step ${index + 1}`} onClick={onRemove}>
          <X size={16} aria-hidden />
        </Button>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        {names.length > 0 ? (
          <Select
            label="Command"
            value={step.command}
            placeholder="Choose a command"
            hint={meta?.description || undefined}
            onValueChange={onCommand}
            // A saved command this node doesn't list stays selectable.
            options={[...(step.command && !meta ? [step.command] : []), ...names].map((n) => ({ value: n, label: n }))}
          />
        ) : (
          <Input label="Command" value={step.command} placeholder="e.g. get_weather" onChange={(e) => onCommand(e.target.value)} />
        )}
        <Input label="Label" hint="Its name in the reply; the command when empty." value={step.label} onChange={(e) => onChange({ ...step, label: e.target.value })} />
      </div>
      {step.args.length > 0 && (
        <div className="space-y-3">
          {step.args.map((a, i) => (
            <ArgField
              key={i}
              arg={a}
              meta={meta?.parameters.find((p) => p.name === a.key) ?? null}
              onChange={(u) => setArg(i, u)}
              onRemove={() => onChange({ ...step, args: step.args.filter((_, j) => j !== i) })}
            />
          ))}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {unused.length > 0 && (
          <Select
            aria-label={`Add a parameter to step ${index + 1}`}
            value=""
            placeholder="Add a parameter…"
            wrapperClassName="w-auto"
            onValueChange={(name) => {
              const p = unused.find((x) => x.name === name)
              if (p) onChange({ ...step, args: [...step.args, { key: p.name, value: p.default_value ?? '' }] })
            }}
            options={unused.map((p) => ({ value: p.name, label: p.description ? `${p.name}: ${p.description}` : p.name }))}
          />
        )}
        <Button size="sm" variant="ghost" icon={<Plus size={14} aria-hidden />} onClick={() => onChange({ ...step, args: [...step.args, { key: '', value: '' }] })}>
          Custom argument
        </Button>
      </div>
    </section>
  )
}

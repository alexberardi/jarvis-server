import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { listSignalAutomations, putSignalAutomation, type Delivery, type SignalAutomation } from '@/api/household'
import { Alert, Badge, Button, Card, CardHeader, ChoiceGroup, SkeletonList, Switch } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

const DELIVERY: { value: Delivery; label: string }[] = [
  { value: 'notification', label: 'Ask first' },
  { value: 'automatic', label: 'Just do it' },
]

const HINT: Record<Delivery, string> = {
  notification: 'Jarvis sends you a card to confirm before it acts.',
  automatic: 'Jarvis acts the moment it happens. Anything sensitive, like unlocking a door, still asks first.',
}

/**
 * SignalAutomations is what Jarvis does when something happens (mobile's Automations): an
 * instruction per signal kind, how it is delivered, and on or off. Any member reads them; an
 * admin edits and saves each one. Saving a blank instruction clears the rule.
 */
export default function SignalAutomations({ hh, canEdit }: { hh: string; canEdit: boolean }) {
  const { data, isLoading, error } = useQuery({ queryKey: ['signal-automations', hh], queryFn: () => listSignalAutomations(hh) })

  return (
    <Card as="section" aria-labelledby="household-automations" className="space-y-4">
      <CardHeader
        id="household-automations"
        title="When something happens"
        description={
          canEdit
            ? 'Tell Jarvis in your own words what to do, and whether to check with you first.'
            : 'What Jarvis does when something happens. Only a household admin can change these.'
        }
      />
      {isLoading && <SkeletonList rows={3} />}
      {error && (
        <Alert tone="danger" title="Couldn't load the automations">
          {errorMessage(error, 'Reload the page to try again.')}
        </Alert>
      )}
      {data && (
        <ul className="divide-y divide-border">
          {data.map((a) => (
            <li key={a.kind} className="py-4 first:pt-0 last:pb-0">
              {canEdit ? <Editor hh={hh} a={a} /> : <Summary a={a} />}
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}

function Heading({ a, id }: { a: SignalAutomation; id: string }) {
  return (
    <div className="space-y-0.5">
      <div className="flex flex-wrap items-center gap-2">
        <h3 id={id} className="text-body font-medium text-text">
          {a.label}
        </h3>
        {!a.observed && <Badge>Hasn’t happened yet</Badge>}
      </div>
      <p className="text-small text-pretty text-text-muted">{a.description}</p>
    </div>
  )
}

function Summary({ a }: { a: SignalAutomation }) {
  const id = `automation-${a.kind}`
  const live = a.enabled && a.instruction !== ''
  return (
    <div className="space-y-2" aria-labelledby={id} role="group">
      <Heading a={a} id={id} />
      {a.instruction ? (
        <p className="text-body text-pretty text-text">
          “{a.instruction}”{' '}
          <span className="text-small text-text-muted">· {live ? (a.delivery === 'automatic' ? 'runs by itself' : 'asks first') : 'off'}</span>
        </p>
      ) : (
        <p className="text-small text-text-muted">Nothing set.</p>
      )}
    </div>
  )
}

function Editor({ hh, a }: { hh: string; a: SignalAutomation }) {
  const qc = useQueryClient()
  const [draft, setDraft] = useState({ instruction: a.instruction, enabled: a.enabled, delivery: a.delivery })
  const [saving, setSaving] = useState(false)
  const id = `automation-${a.kind}`
  const hasText = draft.instruction.trim() !== ''
  const dirty = draft.instruction.trim() !== a.instruction || draft.enabled !== a.enabled || draft.delivery !== a.delivery

  async function save() {
    setSaving(true)
    try {
      const res = await putSignalAutomation(hh, a.kind, { ...draft, instruction: draft.instruction.trim() })
      const next = { instruction: res.instruction, enabled: res.enabled, delivery: res.delivery }
      qc.setQueryData<SignalAutomation[]>(['signal-automations', hh], (old) => old?.map((x) => (x.kind === a.kind ? { ...x, ...next } : x)))
      setDraft(next)
      toast.success(res.instruction ? 'Saved' : 'Cleared')
    } catch (err) {
      toast.error(errorMessage(err, 'Could not save it'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form
      className="space-y-3"
      aria-labelledby={id}
      onSubmit={(e) => {
        e.preventDefault()
        void save()
      }}
    >
      <Heading a={a} id={id} />
      <label htmlFor={`${id}-text`} className="sr-only">
        What Jarvis should do when {a.label.toLowerCase()}
      </label>
      <textarea
        id={`${id}-text`}
        rows={2}
        className={cn(fieldControl, 'h-auto py-2')}
        value={draft.instruction}
        // The saved reply replaces the draft, so no typing while it's in flight.
        readOnly={saving}
        placeholder={`e.g. ${a.example}`}
        // A rule with no words can't be on: typing switches a new one on, clearing switches it off.
        onChange={(e) => {
          const t = e.target.value
          setDraft((d) => ({ ...d, instruction: t, enabled: t.trim() === '' ? false : d.instruction.trim() === '' ? true : d.enabled }))
        }}
      />
      <div className="space-y-1">
        <ChoiceGroup legend="When it happens" value={draft.delivery} onChange={(v) => setDraft((d) => ({ ...d, delivery: v }))} options={DELIVERY} />
        <p className="text-small text-text-muted">{HINT[draft.delivery]}</p>
      </div>
      <div className="flex items-center justify-between gap-4">
        <Switch label="On" checked={draft.enabled && hasText} disabled={!hasText} onCheckedChange={(v) => setDraft((d) => ({ ...d, enabled: v }))} />
        <Button type="submit" loading={saving} disabled={!dirty}>
          {hasText || !a.instruction ? 'Save' : 'Clear'}
        </Button>
      </div>
    </form>
  )
}

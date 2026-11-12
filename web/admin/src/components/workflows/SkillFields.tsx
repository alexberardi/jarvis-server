import { useId } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import type { Prerequisite, PrerequisiteKind, Skill } from '@/api/workflows'
import { Button, Card, CardHeader, Input, Select } from '@/components/ui'
import { FieldFrame } from '@/components/ui/Field'
import { fieldControl } from '@/components/ui/fieldStyles'
import { PREREQS, prereqField, prereqSentence } from '@/lib/workflows'
import { cn } from '@/lib/utils'

/** TextArea is a two-line field on the base components' frame and field style. */
export function TextArea({
  label,
  ariaLabel,
  hint,
  value,
  maxLength,
  className,
  onChange,
}: {
  /** The visible label; ariaLabel names a field in a row that has none. */
  label?: string
  ariaLabel?: string
  hint?: string
  value: string
  maxLength?: number
  className?: string
  onChange: (v: string) => void
}) {
  const id = useId()
  return (
    <FieldFrame id={id} label={label} hint={hint} className={className}>
      <textarea
        id={id}
        rows={2}
        aria-label={ariaLabel}
        maxLength={maxLength}
        className={cn(fieldControl, 'h-auto py-2')}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </FieldFrame>
  )
}

const said = (label: string, value?: string) =>
  value?.trim() ? (
    <div>
      <dt className="text-small text-text-muted">{label}</dt>
      <dd className="text-body text-text [overflow-wrap:anywhere]">{value}</dd>
    </div>
  ) : null

/**
 * GoalCard is what the task is for and what is true once it worked: learnt from the recording,
 * corrected here. A finished run is checked against the end state.
 */
export function GoalCard({ skill, edit, onChange }: { skill: Skill; edit: boolean; onChange: (s: Skill) => void }) {
  const goal = skill.goal ?? {}
  const empty = !goal.reason?.trim() && !goal.end_state?.trim() && !skill.when_to_use?.trim()
  return (
    <Card as="section" aria-labelledby="wf-goal" className="space-y-4">
      <CardHeader
        id="wf-goal"
        title="What it's for"
        description={!edit && empty ? 'Nothing is written down yet.' : undefined}
        className={!edit && empty ? 'mb-0' : 'mb-2'}
      />
      {edit ? (
        <>
          <TextArea
            label="Why you do this"
            maxLength={500}
            value={goal.reason ?? ''}
            onChange={(v) => onChange({ ...skill, goal: { ...goal, reason: v } })}
          />
          <TextArea
            label="It's done when"
            hint="What you could see on the screen or in your files. Jarvis checks a finished run against this."
            maxLength={500}
            value={goal.end_state ?? ''}
            onChange={(v) => onChange({ ...skill, goal: { ...goal, end_state: v } })}
          />
          <Input
            label="Use it when"
            maxLength={500}
            value={skill.when_to_use ?? ''}
            onChange={(e) => onChange({ ...skill, when_to_use: e.target.value })}
          />
        </>
      ) : (
        !empty && (
          <dl className="space-y-3">
            {said('Why', goal.reason)}
            {said("It's done when", goal.end_state)}
            {said('Use it when', skill.when_to_use)}
          </dl>
        )
      )}
    </Card>
  )
}

/**
 * PrerequisitesCard is what must be true before the first step. They come from where the
 * recording began; the owner keeps, drops or adds. A value that changes each run is `{name}`.
 */
export function PrerequisitesCard({ skill, edit, onChange }: { skill: Skill; edit: boolean; onChange: (s: Skill) => void }) {
  const list = skill.prerequisites ?? []
  const set = (prerequisites: Prerequisite[]) => onChange({ ...skill, prerequisites })
  const patch = (i: number, p: Prerequisite) => set(list.map((x, j) => (j === i ? p : x)))
  return (
    <Card as="section" aria-labelledby="wf-before">
      <CardHeader
        id="wf-before"
        title="Before it starts"
        description={
          list.length === 0
            ? 'Nothing has to be set up first.'
            : edit
              ? 'Jarvis makes sure of each one before the first step. Write a value that changes each time as {name}.'
              : undefined
        }
        className={list.length === 0 && !edit ? 'mb-0' : undefined}
      />
      {list.length > 0 && (
        <ul className={edit ? 'space-y-3' : 'list-disc space-y-1 pl-5'}>
          {list.map((p, i) => {
            const field = prereqField(p.kind)
            const kind = PREREQS.find((k) => k.value === p.kind) ?? PREREQS[4]
            return edit ? (
              <li key={i} data-testid="prerequisite" className="flex flex-col gap-2 sm:flex-row sm:items-center">
                <Select
                  aria-label={`Prerequisite ${i + 1}`}
                  wrapperClassName="sm:w-44"
                  options={PREREQS.map((k) => ({ value: k.value, label: k.label }))}
                  value={p.kind}
                  // The value moves to the new kind's field, so nothing typed is lost.
                  onValueChange={(v) => patch(i, { kind: v as PrerequisiteKind, [prereqField(v as PrerequisiteKind)]: p[field] ?? '' })}
                />
                <div className="flex flex-1 items-center gap-2">
                  <Input
                    aria-label={`${kind.label} (prerequisite ${i + 1})`}
                    wrapperClassName="flex-1"
                    placeholder={kind.placeholder}
                    maxLength={500}
                    value={p[field] ?? ''}
                    onChange={(e) => patch(i, { ...p, [field]: e.target.value })}
                  />
                  <Button
                    size="icon"
                    variant="ghost"
                    className="hover:bg-danger-soft hover:text-danger"
                    aria-label={`Remove prerequisite ${i + 1}`}
                    onClick={() => set(list.filter((_, j) => j !== i))}
                  >
                    <Trash2 size={16} aria-hidden />
                  </Button>
                </div>
              </li>
            ) : (
              <li key={i} className="text-body text-text [overflow-wrap:anywhere]">
                {prereqSentence(p)}
              </li>
            )
          })}
        </ul>
      )}
      {edit && (
        <Button
          className={list.length ? 'mt-4' : undefined}
          icon={<Plus size={16} aria-hidden />}
          disabled={list.length >= 20}
          onClick={() => set([...list, { kind: 'app_open', app: '' }])}
        >
          Add a prerequisite
        </Button>
      )}
    </Card>
  )
}

/** NotesCard is what to watch for when doing the task; a note about an app keeps its app. */
export function NotesCard({ skill, edit, onChange }: { skill: Skill; edit: boolean; onChange: (s: Skill) => void }) {
  const notes = skill.notes ?? []
  const set = (n: NonNullable<Skill['notes']>) => onChange({ ...skill, notes: n })
  if (!edit && notes.length === 0) return null
  return (
    <Card as="section" aria-labelledby="wf-notes">
      <CardHeader
        id="wf-notes"
        title="Things to watch for"
        description={notes.length === 0 ? 'Anything Jarvis should know that the steps do not show.' : undefined}
      />
      {notes.length > 0 && (
        <ul className={edit ? 'space-y-3' : 'list-disc space-y-1 pl-5'}>
          {notes.map((n, i) =>
            edit ? (
              <li key={i} className="flex items-start gap-2">
                <TextArea
                  ariaLabel={`Note ${i + 1}`}
                  className="flex-1"
                  maxLength={500}
                  value={n.text}
                  onChange={(v) => set(notes.map((x, j) => (j === i ? { ...x, text: v } : x)))}
                />
                <Button
                  size="icon"
                  variant="ghost"
                  className="hover:bg-danger-soft hover:text-danger"
                  aria-label={`Remove note ${i + 1}`}
                  onClick={() => set(notes.filter((_, j) => j !== i))}
                >
                  <Trash2 size={16} aria-hidden />
                </Button>
              </li>
            ) : (
              <li key={i} className="text-body text-text [overflow-wrap:anywhere]">
                {n.text}
              </li>
            ),
          )}
        </ul>
      )}
      {edit && (
        <Button
          className={notes.length ? 'mt-4' : undefined}
          icon={<Plus size={16} aria-hidden />}
          disabled={notes.length >= 50}
          onClick={() => set([...notes, { text: '' }])}
        >
          Add a note
        </Button>
      )}
    </Card>
  )
}

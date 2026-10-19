import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, Trash2, Video } from 'lucide-react'
import { toast } from 'sonner'
import { getHouseholdSettings, putHouseholdSetting } from '@/api/household'
import { RETENTION_KEY, deleteRecording } from '@/api/workflows'
import type { Recording } from '@/api/workflows'
import Sentence from '@/components/workflows/Sentence'
import { useAuth } from '@/hooks/useAuth'
import { useRecording, useRecordings } from '@/hooks/useWorkflows'
import { errorMessage } from '@/lib/errors'
import { formatServerTime } from '@/lib/time'
import { cn } from '@/lib/utils'
import { eventSentence, expiresIn, traceEvents } from '@/lib/workflows'

const button =
  'inline-flex items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:opacity-50'
const section = 'rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-6'
const ADMIN_ROLES = new Set(['admin', 'owner'])
/**
 * RecordingsPage is the caller's own recordings of tasks they showed Jarvis (no one else's, not
 * even a household admin's): what was recorded, viewing and deleting one, and how long they are
 * kept (a household setting only a household admin changes).
 */
export default function RecordingsPage() {
  const list = useRecordings()
  const qc = useQueryClient()
  const [open, setOpen] = useState<string | null>(null)
  const remove = useMutation({
    mutationFn: deleteRecording,
    onSuccess: () => {
      toast.success('Recording deleted')
      void qc.invalidateQueries({ queryKey: ['recordings'] })
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not delete the recording')),
  })
  const items = list.data ?? []

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-xl font-semibold text-[var(--color-text)]">Recordings</h1>
        <Link to="/workflows" className="text-sm text-[var(--color-primary)] hover:underline">
          Workflows
        </Link>
      </div>
      <p className="text-sm text-[var(--color-text-muted)]">
        What you showed Jarvis on your computer. Only you see your recordings; passwords and secure fields are never
        recorded.
      </p>

      <Retention />

      {list.isError ? (
        <p role="alert" className="text-sm text-red-500">
          {errorMessage(list.error, 'Could not load the recordings')}
        </p>
      ) : list.isLoading ? (
        <p className="text-sm text-[var(--color-text-muted)]">Loading...</p>
      ) : items.length === 0 ? (
        <div className="flex flex-col items-center rounded-xl border border-dashed border-[var(--color-border)] p-10 text-center">
          <Video size={32} className="mb-2 text-[var(--color-text-muted)]" aria-hidden="true" />
          <p className="text-sm text-[var(--color-text-muted)]">No recordings.</p>
        </div>
      ) : (
        <ul className="divide-y divide-[var(--color-border)] overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
          {items.map((r) => {
            const label = `Recording from ${formatServerTime(r.created_at, { dateStyle: 'medium', timeStyle: 'short' })}`
            const isOpen = open === r.id
            return (
              <li key={r.id} className="p-3" data-testid="recording">
                <div className="flex items-center gap-3">
                  <button
                    type="button"
                    aria-expanded={isOpen}
                    aria-controls={`rec-${r.id}`}
                    onClick={() => setOpen(isOpen ? null : r.id)}
                    className="flex min-w-0 flex-1 items-start gap-2 rounded text-left"
                  >
                    {isOpen ? (
                      <ChevronDown size={16} className="mt-0.5 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                    ) : (
                      <ChevronRight size={16} className="mt-0.5 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                    )}
                    <span className="min-w-0">
                      <span className="block text-sm font-medium text-[var(--color-text)]">{label}</span>
                      <span className="mt-0.5 flex flex-wrap gap-x-3 gap-y-1 text-xs text-[var(--color-text-muted)]">
                        <span>{r.mode === 'desktop' ? 'Desktop' : 'Browser'}</span>
                        <span>
                          {r.steps} {r.steps === 1 ? 'step' : 'steps'}
                        </span>
                        <span title={formatServerTime(r.expires_at)}>Deleted {expiresIn(r.expires_at)}</span>
                      </span>
                    </span>
                  </button>
                  <button
                    type="button"
                    className={cn(button, 'text-red-500')}
                    aria-label={`Delete ${label}`}
                    disabled={remove.isPending}
                    onClick={() => {
                      if (window.confirm('Delete this recording? The computer that recorded it deletes its copy too.')) remove.mutate(r.id)
                    }}
                  >
                    <Trash2 size={14} aria-hidden="true" /> Delete
                  </button>
                </div>
                {isOpen && <RecordingView id={r.id} summary={r} />}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

function RecordingView({ id, summary }: { id: string; summary: Recording }) {
  const rec = useRecording(id)
  const events = traceEvents(rec.data?.trace)
  return (
    <div id={`rec-${id}`} className="mt-3 space-y-2 pl-6">
      {rec.isError ? (
        <p role="alert" className="text-sm text-red-500">
          {errorMessage(rec.error, 'Could not load the recording')}
        </p>
      ) : rec.isLoading ? (
        <p className="text-sm text-[var(--color-text-muted)]">Loading...</p>
      ) : (
        <>
          {events.length ? (
            <ol className="list-decimal space-y-1 pl-5 text-sm text-[var(--color-text)] [overflow-wrap:anywhere]">
              {events.map((e, i) => (
                <li key={i}>
                  <Sentence parts={eventSentence(e)} />
                </li>
              ))}
            </ol>
          ) : (
            <p className="text-sm text-[var(--color-text-muted)]">Nothing was recorded.</p>
          )}
          {rec.data?.narration && (
            <p className="text-sm text-[var(--color-text-muted)]">
              You said: <q>{rec.data.narration}</q>
            </p>
          )}
          {summary.keyframes > 0 && (
            <p className="text-xs text-[var(--color-text-muted)]">
              {summary.keyframes} {summary.keyframes === 1 ? 'keyframe' : 'keyframes'}, kept only on the computer that recorded it.
            </p>
          )}
        </>
      )}
    </div>
  )
}

/** Retention is how long recordings are kept: any member reads it, a household admin changes it. */
function Retention() {
  const { households, activeHouseholdId: hh } = useAuth()
  const qc = useQueryClient()
  const membership = households.find((h) => h.id === hh)
  const canEdit = !!membership && ADMIN_ROLES.has(membership.role)
  const queryKey = ['household-settings', hh]
  const settings = useQuery({ queryKey, queryFn: () => getHouseholdSettings(hh), enabled: !!hh })
  const current = typeof settings.data?.[RETENTION_KEY] === 'number' ? (settings.data[RETENTION_KEY] as number) : null
  const [draft, setDraft] = useState<string | null>(null)
  const value = draft ?? (current === null ? '' : String(current))
  const days = Number(value)
  const valid = Number.isInteger(days) && days >= 1 && days <= 3650
  const write = useMutation({
    mutationFn: () => putHouseholdSetting(hh, RETENTION_KEY, days),
    onSuccess: () => {
      qc.setQueryData<Record<string, unknown>>(queryKey, (old) => ({ ...old, [RETENTION_KEY]: days }))
      setDraft(null)
      toast.success('Saved')
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not save the setting')),
  })
  if (!hh) return null

  return (
    <section aria-labelledby="rec-retention" className={section}>
      <h2 id="rec-retention" className="text-lg font-medium text-[var(--color-text)]">
        How long recordings are kept
      </h2>
      {settings.isError ? (
        <p role="alert" className="mt-2 text-sm text-red-500">
          {errorMessage(settings.error, 'Could not load the setting')}
        </p>
      ) : canEdit ? (
        <form
          className="mt-2 flex flex-wrap items-center gap-2 text-sm text-[var(--color-text)]"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) write.mutate()
          }}
        >
          <label htmlFor="rec-retention-days">Keep for</label>
          <input
            id="rec-retention-days"
            type="number"
            min={1}
            max={3650}
            value={value}
            onChange={(e) => setDraft(e.target.value)}
            className="w-24 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-1.5 text-sm text-[var(--color-text)] outline-none focus:ring-2 focus:ring-[var(--color-primary)]"
          />
          <span>days</span>
          <button type="submit" className={button} disabled={!valid || draft === null || write.isPending}>
            Save
          </button>
        </form>
      ) : (
        <p className="mt-2 text-sm text-[var(--color-text)]">
          {current === null ? '...' : `${current} days.`}{' '}
          <span className="text-[var(--color-text-muted)]">Only a household admin can change this.</span>
        </p>
      )}
      <p className="mt-1 text-xs text-[var(--color-text-muted)]">
        For the whole household, here and on the computer that recorded it. A change applies to new recordings.
      </p>
    </section>
  )
}

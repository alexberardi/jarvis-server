import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronRight, Trash2, Video, Workflow } from 'lucide-react'
import { toast } from 'sonner'
import { getHouseholdSettings, putHouseholdSetting } from '@/api/household'
import { RETENTION_KEY, deleteRecording, getRecordingKeyframe } from '@/api/workflows'
import type { Recording, TraceEvent } from '@/api/workflows'
import { Button, Card, Dialog, EmptyState, ErrorState, Input, List, PageHeader, Skeleton, SkeletonList } from '@/components/ui'
import CrossLink from '@/components/workflows/CrossLink'
import Sentence from '@/components/workflows/Sentence'
import { useAuth } from '@/hooks/useAuth'
import { useRecording, useRecordings } from '@/hooks/useWorkflows'
import { errorMessage } from '@/lib/errors'
import { formatServerTime } from '@/lib/time'
import { cn } from '@/lib/utils'
import { eventSentence, expiresIn, foldTrace, traceEvents } from '@/lib/workflows'

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
      <PageHeader
        title="Recordings"
        description="What you showed Jarvis on your computer. Only you see your recordings; passwords and secure fields are never recorded."
        actions={
          <CrossLink to="/workflows" icon={<Workflow size={16} aria-hidden />}>
            Workflows
          </CrossLink>
        }
      />

      <Retention />

      <Card flush className="overflow-hidden">
        {list.isError ? (
          <ErrorState
            title="Couldn't load your recordings"
            message={errorMessage(list.error, 'Check your connection and try again.')}
            onRetry={() => list.refetch()}
          />
        ) : list.isLoading ? (
          <SkeletonList rows={2} label="Loading your recordings" />
        ) : items.length === 0 ? (
          <EmptyState
            icon={<Video size={20} />}
            title="No recordings yet"
            description="On your computer, pick “Record a macro…” in the Jarvis Automation menu or say “Jarvis, watch me”, then do the task once."
          />
        ) : (
          <List label="Recordings">
            {items.map((r) => {
              const label = `Recording from ${formatServerTime(r.created_at, { dateStyle: 'medium', timeStyle: 'short' })}`
              const isOpen = open === r.id
              return (
                <li key={r.id} className="px-4 py-3" data-testid="recording">
                  <div className="flex items-center gap-3">
                    <button
                      type="button"
                      aria-expanded={isOpen}
                      aria-controls={`rec-${r.id}`}
                      onClick={() => setOpen(isOpen ? null : r.id)}
                      className="flex min-h-10 min-w-0 flex-1 items-start gap-2 rounded-control text-left"
                    >
                      <ChevronRight
                        size={16}
                        aria-hidden
                        className={cn(
                          'mt-0.5 shrink-0 text-text-muted transition-transform duration-(--duration-menu) motion-reduce:transition-none',
                          isOpen && 'rotate-90',
                        )}
                      />
                      <span className="min-w-0">
                        <span className="block text-subheading text-text">{label}</span>
                        <span className="mt-0.5 flex flex-wrap gap-x-3 gap-y-1 text-small text-text-muted">
                          <span>{r.mode === 'desktop' ? 'Desktop' : 'Browser'}</span>
                          <span>
                            {r.steps} {r.steps === 1 ? 'step' : 'steps'}
                          </span>
                          <span title={formatServerTime(r.expires_at)}>Deleted {expiresIn(r.expires_at)}</span>
                        </span>
                      </span>
                    </button>
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<Trash2 size={16} aria-hidden />}
                      aria-label={`Delete ${label}`}
                      disabled={remove.isPending}
                      onClick={() => {
                        if (window.confirm('Delete this recording? The computer that recorded it deletes its copy too.')) remove.mutate(r.id)
                      }}
                    >
                      Delete
                    </Button>
                  </div>
                  {isOpen && <RecordingView id={r.id} summary={r} />}
                </li>
              )
            })}
          </List>
        )}
      </Card>
    </div>
  )
}

function RecordingView({ id, summary }: { id: string; summary: Recording }) {
  const rec = useRecording(id)
  const events = foldTrace(traceEvents(rec.data?.trace))
  const stored = new Map((rec.data?.keyframe_images ?? []).map((k) => [k.seq, k.crop]))
  return (
    <div id={`rec-${id}`} className="mt-3 space-y-2 pl-6">
      {rec.isError ? (
        <p role="alert" className="text-small text-danger">
          {errorMessage(rec.error, "Couldn't load this recording.")}
        </p>
      ) : rec.isLoading ? (
        <div role="status" aria-label="Loading the recording" className="space-y-2">
          <Skeleton className="w-3/5" />
          <Skeleton className="w-2/5" />
        </div>
      ) : (
        <>
          {events.length ? (
            <ol className="list-decimal space-y-1 pl-5 text-body text-text marker:text-text-muted [overflow-wrap:anywhere]">
              {events.map((e, i) => (
                <li key={i}>
                  <Sentence parts={eventSentence(e)} />
                  <Where e={e} />
                  {e.seq !== undefined && stored.has(e.seq) && (
                    <Keyframe id={id} seq={e.seq} crop={!!stored.get(e.seq)} step={i + 1} />
                  )}
                </li>
              ))}
            </ol>
          ) : (
            <p className="text-small text-text-muted">Nothing was recorded.</p>
          )}
          {rec.data?.narration && (
            <p className="text-small text-text-muted">
              You said: <q>{rec.data.narration}</q>
            </p>
          )}
          {summary.keyframes > 0 && (
            <p className="text-caption text-text-muted">
              {summary.keyframes} {summary.keyframes === 1 ? 'keyframe' : 'keyframes'}, {stored.size} kept on the server.
            </p>
          )}
        </>
      )}
    </div>
  )
}

/** Where is the app and window a desktop event happened in ("in Firefox — Statements"). */
function Where({ e }: { e: TraceEvent }) {
  const where = [e.app, e.window?.title].filter(Boolean).join(' — ')
  return where ? <span className="block text-small text-text-muted">in {where}</span> : null
}

/** useKeyframeURL downloads a keyframe (or its crop) once wanted, as an object URL. */
function useKeyframeURL(id: string, seq: number, crop: boolean, wanted = true) {
  const [url, setURL] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!wanted) return
    let live = true
    let made: string | null = null
    getRecordingKeyframe(id, seq, crop)
      .then((blob) => {
        if (!live) return
        made = URL.createObjectURL(blob)
        setURL(made)
      })
      .catch(() => live && setFailed(true))
    return () => {
      live = false
      if (made) URL.revokeObjectURL(made)
    }
  }, [id, seq, crop, wanted])
  return { url, failed }
}

/** Keyframe is a click's stored keyframe: its crop as a thumbnail, the whole marked screen on click. */
function Keyframe({ id, seq, crop, step }: { id: string; seq: number; crop: boolean; step: number }) {
  const [open, setOpen] = useState(false)
  const thumb = useKeyframeURL(id, seq, crop)
  // Without a crop the thumbnail is the whole keyframe already.
  const big = useKeyframeURL(id, seq, false, open && crop)
  const full = crop ? big : thumb
  if (thumb.failed) return <span className="block text-small text-text-muted">Couldn't load its keyframe.</span>
  if (!thumb.url) return <Skeleton className="mt-1 h-20 w-20" />
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="mt-1 block rounded-control border border-border"
      >
        <img src={thumb.url} alt={`Where you clicked in step ${step}: show the whole screen`} className="h-20 w-auto rounded-control" />
      </button>
      <Dialog open={open} onClose={() => setOpen(false)} title={`Step ${step}`} className="md:max-w-4xl">
        {full.failed ? (
          <p role="alert" className="text-small text-danger">
            Couldn't load the keyframe.
          </p>
        ) : full.url ? (
          <img src={full.url} alt={`The screen in step ${step}, the click marked`} className="h-auto max-w-full" />
        ) : (
          <Skeleton className="h-64 w-full" />
        )}
      </Dialog>
    </>
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
    <Card as="section" aria-labelledby="rec-retention" className="space-y-3">
      <h2 id="rec-retention" className="text-heading text-text">
        How long recordings are kept
      </h2>
      {settings.isError ? (
        <p role="alert" className="text-small text-danger">
          {errorMessage(settings.error, "Couldn't load the setting.")}
        </p>
      ) : canEdit ? (
        <form
          className="flex flex-wrap items-center gap-2 text-body text-text"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) write.mutate()
          }}
        >
          <label htmlFor="rec-retention-days">Keep for</label>
          <Input
            id="rec-retention-days"
            type="number"
            min={1}
            max={3650}
            value={value}
            onChange={(e) => setDraft(e.target.value)}
            wrapperClassName="w-24"
          />
          <span>days</span>
          <Button type="submit" disabled={!valid || draft === null} loading={write.isPending}>
            Save
          </Button>
        </form>
      ) : (
        <p className="text-body text-text">
          {current === null ? '...' : `${current} days.`}{' '}
          <span className="text-text-muted">Only a household admin can change this.</span>
        </p>
      )}
      <p className="text-small text-text-muted">
        For the whole household, here and on the computer that recorded it. A change applies to new recordings.
      </p>
    </Card>
  )
}

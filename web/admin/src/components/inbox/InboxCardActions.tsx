import { useEffect, useRef, useState } from 'react'
import { AlertCircle, Check, Clapperboard, Film, User } from 'lucide-react'
import { toast } from 'sonner'
import { callbackStatus, failureMessage, normalizeInteractiveElements, sendCallback } from '@/api/callbacks'
import type { InteractiveElement } from '@/api/callbacks'
import type { InboxItem } from '@/api/inbox'
import { Button, Card, Input } from '@/components/ui'
import { FieldFrame } from '@/components/ui/Field'
import { fieldControl } from '@/components/ui/fieldStyles'
import { errorMessage } from '@/lib/errors'
import { isExpiryError, parseExpiresAt, parseInboxEditors, type EditorField } from '@/lib/inbox/editors'
import { cn } from '@/lib/utils'

// A tap is watched until its job finishes, as mobile does, so a failure shows instead of a tick.
const POLL_MS = 1000
const POLL_TIMEOUT_MS = 30_000
// A server-plane tap may rewrite this card in place (a re-plan bumps metadata.revision).
const REFRESH_MS = 1500
const REFRESH_ATTEMPTS = 20

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

const kindIcon = (kind?: string) =>
  kind === 'actor' ? <User size={14} aria-hidden /> : kind === 'movie' ? <Film size={14} aria-hidden /> : kind === 'director' ? <Clapperboard size={14} aria-hidden /> : undefined

/** A datetime-local value without seconds gets them back: producers send "YYYY-MM-DDTHH:MM:SS". */
const withSeconds = (v: string) => (/^\d{4}-\d\d-\d\dT\d\d:\d\d$/.test(v) ? `${v}:00` : v)

interface Props {
  item: InboxItem
  /** Refetches the item; the card polls it after a tap until its revision changes. */
  refresh: () => Promise<InboxItem | undefined>
}

/**
 * InboxCardActions is an item's editable fields and tappable elements
 * (metadata.editable_fields / editable_text and metadata.interactive_elements), with mobile's rules:
 * an element whose data carries a field's data_key sends the live value; a required empty field
 * blocks the tap; a card this build can't fully render, or an expired one, disables every element.
 */
export default function InboxCardActions({ item, refresh }: Props) {
  const m = item.metadata ?? {}
  const elements = normalizeInteractiveElements(m.interactive_elements)
  const editors = parseInboxEditors(m)
  const expiresAt = parseExpiresAt(m)
  const nodeId = typeof m.node_id === 'string' && m.node_id ? m.node_id : null

  const [values, setValues] = useState<Record<string, string>>(() => initialValues(editors.fields))
  const [pending, setPending] = useState<string | null>(null)
  const [sent, setSent] = useState<Set<string>>(new Set())
  const [failed, setFailed] = useState<Set<string>>(new Set())
  const [fieldError, setFieldError] = useState<string | null>(null)
  const [expiredByServer, setExpiredByServer] = useState(false)
  const [now, setNow] = useState(() => Date.now())
  const mounted = useRef(true)
  useEffect(() => () => void (mounted.current = false), [])

  // A card that expires while open turns itself off.
  const expiresMs = expiresAt?.getTime() ?? null
  useEffect(() => {
    if (expiresMs === null || expiresMs <= now) return
    const t = setTimeout(() => setNow(Date.now()), Math.min(expiresMs - now + 50, 2 ** 31 - 1))
    return () => clearTimeout(t)
  }, [expiresMs, now])
  const expired = expiredByServer || (expiresMs !== null && expiresMs <= now)

  if (elements.length === 0 && editors.fields.length === 0 && !editors.unsupported) return null

  async function tap(el: InteractiveElement) {
    const server = el.target === 'server'
    let data = el.data
    for (const f of editors.fields) {
      if (!Object.prototype.hasOwnProperty.call(el.data, f.data_key)) continue
      const v = values[f.data_key] ?? ''
      if (f.required && !v.trim()) {
        setFieldError(f.legacy ? 'Draft is empty' : `${f.label ?? f.data_key} is required`)
        return
      }
      data = { ...data, [f.data_key]: f.input_type === 'datetime' ? withSeconds(v) : v }
    }
    setFieldError(null)
    setPending(el.id)
    setFailed((s) => without(s, el.id))
    const startRev = m.revision ?? null
    try {
      const job = await sendCallback({
        command_name: el.command,
        callback_name: el.callback,
        data,
        // Every producer sends new_notification; stack and popover are watched the same way here.
        navigation_type: el.navigation_type ?? 'new_notification',
        ...(server ? { household_id: item.household_id } : { target_node_id: nodeId as string }),
      })
      const outcome = await awaitOutcome(job.id)
      if (!mounted.current) return
      if (outcome) {
        setFailed((s) => new Set(s).add(el.id))
        toast.error(outcome)
      } else {
        setSent((s) => new Set(s).add(el.id))
        void followRevision(startRev)
      }
    } catch (err) {
      if (isExpiryError(err)) setExpiredByServer(true)
      else toast.error(errorMessage(err, 'Could not send it'))
    } finally {
      if (mounted.current) setPending(null)
    }
  }

  async function followRevision(startRev: unknown) {
    for (let i = 0; i < REFRESH_ATTEMPTS && mounted.current; i++) {
      await sleep(REFRESH_MS)
      const next = await refresh().catch(() => undefined)
      const rev = next?.metadata?.revision ?? null
      if (startRev === null || (rev !== null && rev !== startRev)) return
    }
  }

  const locked = editors.unsupported || expired
  // Only fields or a notice earn a box; a row of buttons on its own sits on the page.
  const boxed = editors.fields.length > 0 || locked
  const body = (
    <>
      {editors.unsupported && (
        <p role="note" className="text-body text-text-muted">
          This card has fields this page can't show yet, so its buttons are turned off rather than sending without them.
        </p>
      )}
      {expired && (
        <p role="status" className="text-body text-text-muted">
          This card has expired. Ask Jarvis again to get a fresh one.
        </p>
      )}
      {editors.fields.map((f) => (
        <EditorInput
          key={f.key}
          field={f}
          value={values[f.data_key] ?? ''}
          disabled={pending !== null || locked}
          onChange={(v) => setValues((s) => ({ ...s, [f.data_key]: v }))}
        />
      ))}
      {fieldError && (
        <p role="alert" className="text-small text-danger">
          {fieldError}
        </p>
      )}
      {elements.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {elements.map((el) => {
            const isSent = sent.has(el.id)
            const isFailed = failed.has(el.id)
            const noContext = el.target === 'server' ? !item.household_id : !nodeId
            return (
              <Button
                key={el.id}
                size="sm"
                variant="secondary"
                loading={pending === el.id}
                disabled={locked || noContext || isSent || (pending !== null && pending !== el.id)}
                icon={isSent ? <Check size={14} aria-hidden /> : isFailed ? <AlertCircle size={14} aria-hidden /> : kindIcon(el.kind)}
                className={cn(isFailed && 'border-danger/50 text-danger')}
                onClick={() => void tap(el)}
              >
                {el.sublabel ? `${el.label} · ${el.sublabel}` : el.label}
                {isSent && <span className="sr-only"> (sent)</span>}
              </Button>
            )
          })}
        </div>
      )}
    </>
  )
  return boxed ? (
    <Card as="section" aria-label="Actions" className="space-y-4">
      {body}
    </Card>
  ) : (
    <section aria-label="Actions" className="space-y-4">
      {body}
    </section>
  )
}

function EditorInput({ field, value, disabled, onChange }: { field: EditorField; value: string; disabled: boolean; onChange: (v: string) => void }) {
  const id = `editor-${field.key}`
  const label = field.label ?? (field.legacy ? 'Draft' : field.data_key)
  if (field.input_type === 'multiline')
    return (
      <FieldFrame id={id} label={label}>
        <textarea id={id} rows={4} className={cn(fieldControl, 'h-auto py-2')} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)} />
      </FieldFrame>
    )
  return (
    <Input
      id={id}
      label={label}
      type={field.input_type === 'tel' ? 'tel' : field.input_type === 'datetime' ? 'datetime-local' : 'text'}
      step={field.input_type === 'datetime' ? 1 : undefined}
      value={value}
      disabled={disabled}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}

function initialValues(fields: EditorField[]): Record<string, string> {
  return Object.fromEntries(fields.map((f) => [f.data_key, f.initial]))
}

function without(s: Set<string>, id: string): Set<string> {
  const next = new Set(s)
  next.delete(id)
  return next
}

/** awaitOutcome polls a job until it leaves pending: the failure message, or null (done, or still running at the timeout). */
async function awaitOutcome(jobId: string): Promise<string | null> {
  const started = Date.now()
  for (;;) {
    const s = await callbackStatus(jobId).catch(() => null) // a failed poll is retried until the timeout
    if (s && s.status !== 'pending') return s.status === 'completed' ? null : failureMessage(s)
    if (Date.now() - started >= POLL_TIMEOUT_MS) return null
    await sleep(POLL_MS)
  }
}

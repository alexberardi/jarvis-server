import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Check, CheckCircle2, Copy, ExternalLink, Minus, Plus, Search } from 'lucide-react'
import { toast } from 'sonner'
import { callbackStatus, sendCallback } from '@/api/callbacks'
import { getThisDevice } from '@/api/chat'
import { browserMatches, listRecords, updateRecord, type TabMatch } from '@/api/commandData'
import type { InboxItem } from '@/api/inbox'
import { getNodeLiveness } from '@/api/nodes'
import { Button, Card, Input, buttonStyles, type ButtonVariant } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import {
  MAX_QUANTITY,
  buildCollectedState,
  compileRowActionPattern,
  computeDefaultQuantities,
  computeDefaultSelection,
  computeRowStates,
  currentActionValue,
  parseQuantity,
  shouldShowSelectAll,
  substituteLabel,
  substituteUrl,
  type FetchedRecords,
} from '@/lib/inbox/interactiveList'
import type { ParsedAction, ParsedInteractiveList, ParsedRow, ParsedRowAction } from '@/lib/inbox/interactiveListTypes'
import { cn } from '@/lib/utils'

// As mobile: a job is polled every second for up to 30 s.
const POLL_MS = 1000
const POLL_TIMEOUT_MS = 30_000
// "Find ID" asks the computer every 2 s while its panel is open, for up to 5 minutes.
const MATCH_POLL_MS = 2000
const MATCH_POLL_FOR_MS = 5 * 60_000

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

const VARIANT: Record<ParsedAction['style'], ButtonVariant> = { primary: 'primary', secondary: 'secondary', destructive: 'danger' }

/** A completed action's context_data (mobile's decision 7). Unknown keys are ignored. */
interface ActionResult {
  message?: unknown
  url?: unknown
  text?: unknown
  detail_lines?: unknown
}

interface Props {
  item: InboxItem
  payload: ParsedInteractiveList
  truncated: boolean
}

/**
 * InteractiveList is an interactive_list item (jarvis-command-sdk interactive.py, rendered as
 * jarvis-node-mobile's InteractiveListScreen): sections of rows with checkboxes and quantity
 * steppers, rows gated on a node's stored record, "Find ID" row actions, and action buttons that
 * send the selection to the command's callback on its node, then show the result.
 */
export default function InteractiveList({ item, payload, truncated }: Props) {
  const nodeId = payload.node_id
  const gateCommands = useMemo(() => {
    const s = new Set<string>()
    for (const sec of payload.sections) for (const r of sec.rows) if (r.requires_record_field) s.add(r.requires_record_field.command_name)
    return [...s].sort()
  }, [payload])
  const recordsQ = useQuery({
    queryKey: ['inbox', 'records', nodeId, gateCommands],
    enabled: gateCommands.length > 0,
    staleTime: Infinity,
    queryFn: async (): Promise<FetchedRecords> => {
      const out: FetchedRecords = {}
      // A failed list leaves its gates unmet (null), as mobile: those rows go disabled, the rest works.
      await Promise.all(
        gateCommands.map(async (c) => {
          out[c] = nodeId ? await listRecords(nodeId, c).then((rs) => Object.fromEntries(rs.map((r) => [r.key, r.data ?? {}])), () => null) : null
        }),
      )
      return out
    },
  })
  // The list shows at once; gated rows say "Checking…" until the node answers (it may be off).
  return <ListBody item={item} payload={payload} truncated={truncated} fetched={recordsQ.data ?? EMPTY} checking={recordsQ.isLoading} />
}

const EMPTY: FetchedRecords = {}

function ListBody({ item, payload, truncated, fetched, checking }: Props & { fetched: FetchedRecords; checking: boolean }) {
  const [overrides, setOverrides] = useState<Record<string, string>>({})
  const [selected, setSelected] = useState<Set<string>>(() => computeDefaultSelection(payload, fetched, {}))
  const [quantities, setQuantities] = useState<Record<string, string>>(() => computeDefaultQuantities(payload))
  const [busy, setBusy] = useState<ParsedAction | null>(null)
  const [failed, setFailed] = useState<{ action: ParsedAction; message: string } | null>(null)
  const [result, setResult] = useState<ActionResult | null>(null)
  const [picking, setPicking] = useState<string | null>(null)
  const mounted = useRef(true)
  useEffect(() => () => void (mounted.current = false), [])

  // Records that arrive after the first render select their rows' defaults too (state adjusted
  // while rendering, React's pattern for a changed prop).
  const [seen, setSeen] = useState(fetched)
  if (seen !== fetched) {
    setSeen(fetched)
    setSelected((s) => new Set([...s, ...computeDefaultSelection(payload, fetched, {})]))
  }

  const rowStates = useMemo(() => computeRowStates(payload, fetched, overrides), [payload, fetched, overrides])
  const selectable = payload.sections.flatMap((s) => s.rows.filter((r) => rowStates[r.key]?.selectable).map((r) => r.key))
  const allSelected = selectable.length > 0 && selectable.every((k) => selected.has(k))
  const nodeId = payload.node_id

  const toggle = (key: string) =>
    setSelected((s) => {
      const n = new Set(s)
      if (n.has(key)) n.delete(key)
      else n.add(key)
      return n
    })
  const bump = (key: string, d: number) =>
    setQuantities((q) => ({ ...q, [key]: String(Math.max(1, Math.min(parseQuantity(q[key]) + d, MAX_QUANTITY))) }))

  async function run(action: ParsedAction) {
    if (!nodeId) return
    setBusy(action)
    setFailed(null)
    try {
      const job = await sendCallback({
        command_name: payload.command_name,
        callback_name: action.callback,
        data: { action: action.callback, selected: buildCollectedState(payload, selected, quantities), context: payload.context },
        target_node_id: nodeId,
        navigation_type: 'stack',
      })
      const started = Date.now()
      for (;;) {
        await sleep(POLL_MS)
        if (!mounted.current) return
        const s = await callbackStatus(job.id)
        if (s.status === 'pending') {
          if (Date.now() - started > POLL_TIMEOUT_MS) throw new Error('Timed out waiting for the result.')
          continue
        }
        if (s.status !== 'completed') throw new Error(s.status === 'expired' ? 'This request expired.' : s.error_message || 'Action failed.')
        const r = (s.context_data ?? {}) as ActionResult
        setResult(r)
        // Mobile opens a result's link at once; a browser would block a pop-up this late, so it's a button.
        return
      }
    } catch (err) {
      if (mounted.current) setFailed({ action, message: errorMessage(err, 'Action failed.') })
    } finally {
      if (mounted.current) setBusy(null)
    }
  }

  if (result) return <ResultView result={result} />

  const rowCount = payload.sections.reduce((n, s) => n + s.rows.length, 0)
  if (rowCount === 0)
    return (
      <Card className="text-body text-text-muted" role="status">
        {payload.empty_text ?? 'Nothing here yet.'}
      </Card>
    )

  return (
    <section aria-label={payload.title_override ?? item.title} className="space-y-4">
      {shouldShowSelectAll(payload, rowStates) && (
        <div className="flex items-center justify-between gap-2 text-small text-text-muted">
          <span aria-live="polite">
            {selected.size} of {selectable.length} selected
          </span>
          <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => setSelected(allSelected ? new Set() : new Set(selectable))}>
            {allSelected ? 'Clear' : 'Select all'}
          </Button>
        </div>
      )}

      {payload.sections.map((sec, i) =>
        sec.rows.length === 0 ? null : (
          <Card key={i} flush as="section" aria-label={sec.title ?? undefined}>
            {sec.title && <h2 className="border-b border-border px-4 py-3 text-subheading text-text">{sec.title}</h2>}
            <ul className="divide-y divide-border">
              {sec.rows.map((row) => {
                const st = rowStates[row.key]
                if (!st) return null
                const id = `row-${row.key}`
                return (
                  <li key={row.key} className={cn('px-4 py-3', !st.enabled && 'text-text-muted')}>
                    <div className="flex items-start gap-3">
                      {!st.unknownControl && row.control !== 'none' && (
                        <input
                          id={id}
                          type="checkbox"
                          className="mt-1 size-4 shrink-0 accent-primary"
                          checked={st.selectable && selected.has(row.key)}
                          disabled={!st.selectable || busy !== null}
                          onChange={() => toggle(row.key)}
                        />
                      )}
                      <div className="min-w-0 flex-1">
                        <label htmlFor={id} className={cn('block text-body [overflow-wrap:anywhere]', st.enabled ? 'text-text' : 'text-text-muted')}>
                          {row.label}
                        </label>
                        {checking && row.requires_record_field && !st.enabled ? (
                          <p className="text-small text-text-muted">Checking…</p>
                        ) : (
                          st.caption && <p className="text-small text-text-muted [overflow-wrap:anywhere]">{st.caption}</p>
                        )}
                        {row.row_actions.map((a, ai) => (
                          <RowAction
                            key={ai}
                            row={row}
                            action={a}
                            nodeId={nodeId}
                            value={currentActionValue(row, a, fetched, overrides)}
                            open={picking === `${row.key}:${ai}`}
                            disabled={busy !== null}
                            onOpen={(o) => setPicking(o ? `${row.key}:${ai}` : null)}
                            onPicked={(v) => {
                              setOverrides((s) => ({ ...s, [row.key]: v }))
                              setSelected((s) => new Set(s).add(row.key))
                              setPicking(null)
                            }}
                          />
                        ))}
                      </div>
                      {row.control === 'checkbox_stepper' && st.enabled && (
                        <div className="flex shrink-0 items-center gap-1">
                          <Button size="sm" variant="ghost" aria-label={`One fewer ${row.label}`} icon={<Minus size={14} aria-hidden />} disabled={busy !== null || parseQuantity(quantities[row.key]) <= 1} onClick={() => bump(row.key, -1)} />
                          <input
                            aria-label={`Quantity of ${row.label}`}
                            inputMode="numeric"
                            className="h-10 w-12 rounded-control border border-border-strong bg-surface text-center text-body text-text tabular-nums"
                            value={quantities[row.key] ?? '1'}
                            disabled={busy !== null}
                            onChange={(e) => setQuantities((q) => ({ ...q, [row.key]: e.target.value.replace(/[^0-9]/g, '').slice(0, 2) }))}
                            onBlur={() => setQuantities((q) => ({ ...q, [row.key]: String(parseQuantity(q[row.key])) }))}
                          />
                          <Button size="sm" variant="ghost" aria-label={`One more ${row.label}`} icon={<Plus size={14} aria-hidden />} disabled={busy !== null || parseQuantity(quantities[row.key]) >= MAX_QUANTITY} onClick={() => bump(row.key, 1)} />
                        </div>
                      )}
                    </div>
                  </li>
                )
              })}
            </ul>
          </Card>
        ),
      )}

      {truncated && <p className="text-small text-text-muted">Some of this list was too long to show.</p>}
      {failed && (
        <p role="alert" className="text-small text-danger">
          {failed.message}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        {payload.actions.map((a, i) => (
          <Button
            key={i}
            variant={VARIANT[a.style]}
            loading={busy === a}
            disabled={!nodeId || busy !== null || (selectable.length > 0 && selected.size === 0)}
            onClick={() => void run(a)}
          >
            {failed?.action === a ? 'Retry' : substituteLabel(a.label, selected.size)}
          </Button>
        ))}
      </div>
    </section>
  )
}

function ResultView({ result }: { result: ActionResult }) {
  const [copied, setCopied] = useState(false)
  const lines = Array.isArray(result.detail_lines) ? result.detail_lines.filter((l): l is string => typeof l === 'string') : []
  const url = typeof result.url === 'string' && /^https?:\/\//i.test(result.url) ? result.url : null
  const text = typeof result.text === 'string' && result.text ? result.text : null
  return (
    <Card as="section" aria-label="Result" className="space-y-4">
      <p role="status" className="flex items-center gap-2 text-subheading text-text">
        <CheckCircle2 size={20} className="text-success" aria-hidden /> Done
      </p>
      {typeof result.message === 'string' && result.message && <p className="text-body text-text">{result.message}</p>}
      {text && <pre className="overflow-x-auto rounded-control bg-surface-alt p-3 font-mono text-small whitespace-pre-wrap text-text">{text}</pre>}
      {lines.length > 0 && (
        <ul className="space-y-1.5">
          {lines.map((l, i) => (
            <li key={i} className="flex gap-2 text-body text-text">
              <Check size={16} className="mt-1 shrink-0 text-success" aria-hidden />
              <span className="[overflow-wrap:anywhere]">{l}</span>
            </li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap gap-2">
        {url && (
          <a href={url} target="_blank" rel="noopener noreferrer" className={buttonStyles({ variant: 'primary' })}>
            <ExternalLink size={16} aria-hidden /> Open link
          </a>
        )}
        {text && (
          <Button
            variant="secondary"
            icon={copied ? <Check size={16} aria-hidden /> : <Copy size={16} aria-hidden />}
            onClick={() =>
              void navigator.clipboard.writeText(text).then(
                () => {
                  setCopied(true)
                  setTimeout(() => setCopied(false), 2000)
                },
                () => toast.error('Could not copy it.'),
              )
            }
          >
            {copied ? 'Copied' : 'Copy'}
          </Button>
        )}
      </div>
    </Card>
  )
}

interface RowActionProps {
  row: ParsedRow
  action: ParsedRowAction
  nodeId: string | null
  value: string | null
  open: boolean
  disabled: boolean
  onOpen: (open: boolean) => void
  onPicked: (value: string) => void
}

/**
 * RowAction is a webview_pick ("Find ID"): open the site in a new tab, then pick a value the
 * owner's computer finds in their open tabs (decision 2026-11-15-web-pick-reads-your-tabs), or
 * paste the page's address. The value is saved onto the row's record on the list's node.
 */
function RowAction({ row, action, nodeId, value, open, disabled, onOpen, onPicked }: RowActionProps) {
  const re = compileRowActionPattern(action.pattern)
  if (action.type !== 'webview_pick')
    return (
      <Button size="sm" variant="ghost" disabled className="mt-1">
        {action.label}
      </Button>
    )
  const startUrl = action.start_url ? substituteUrl(action.start_url, { label: row.label, value }) : null
  if (!nodeId || !action.pattern || !action.save || !re || !startUrl) return null
  return (
    <div className="mt-1.5">
      <Button
        size="sm"
        variant="secondary"
        icon={<Search size={14} aria-hidden />}
        aria-expanded={open}
        disabled={disabled}
        onClick={() => {
          if (!open) window.open(startUrl, '_blank', 'noopener')
          onOpen(!open)
        }}
      >
        {action.label}
      </Button>
      {open && (
        <PickPanel
          rowLabel={row.label}
          re={re}
          pattern={action.pattern}
          onPick={async (v) => {
            try {
              await updateRecord(nodeId, action.save!.command_name, row.key, { [action.save!.field]: v })
              onPicked(v)
            } catch (err) {
              toast.error(errorMessage(err, 'Could not save it.'))
            }
          }}
        />
      )}
    </div>
  )
}

function PickPanel({ rowLabel, re, pattern, onPick }: { rowLabel: string; re: RegExp; pattern: string; onPick: (v: string) => Promise<void> }) {
  const { state } = useAuth()
  const computer = useMyComputer(state.user?.id)
  const [pasted, setPasted] = useState('')
  const [saving, setSaving] = useState<string | null>(null)
  const [startedAt] = useState(() => Date.now())
  const matchesQ = useQuery({
    queryKey: ['inbox', 'tab-matches', computer, pattern],
    enabled: !!computer,
    queryFn: () => browserMatches(computer as string, pattern),
    refetchInterval: (q) => (Date.now() - startedAt < MATCH_POLL_FOR_MS && !(q.state.data?.ok === false && q.state.data.error.code === 'bad_pattern') ? MATCH_POLL_MS : false),
    retry: false,
  })
  const pastedValue = pasted.trim() ? (pasted.trim().match(re)?.[1] ?? null) : null
  const pick = async (v: string) => {
    setSaving(v)
    await onPick(v)
    setSaving(null)
  }
  const data = matchesQ.data
  const matches: TabMatch[] = data?.ok ? data.matches : []
  return (
    <div className="mt-2 space-y-3 rounded-control border border-border bg-surface-alt/50 p-3" aria-label={`Find a value for ${rowLabel}`} role="group">
      {computer && (
        <div className="space-y-2" aria-live="polite">
          {matches.length > 0 ? (
            <>
              <p className="text-small text-text-muted">Found in your open tabs:</p>
              <ul className="space-y-1.5">
                {matches.map((m) => (
                  <li key={m.value} className="flex flex-wrap items-center gap-2">
                    <Button size="sm" loading={saving === m.value} disabled={saving !== null} onClick={() => void pick(m.value)}>
                      Use {m.value}
                    </Button>
                    {m.title && <span className="min-w-0 text-small text-text-muted [overflow-wrap:anywhere]">{m.title}</span>}
                  </li>
                ))}
              </ul>
            </>
          ) : data && !data.ok ? (
            <p className="text-small text-text-muted">{data.error.message}</p>
          ) : (
            <p className="text-small text-text-muted">Open the right page in the new tab and Jarvis will find it here.</p>
          )}
        </div>
      )}
      <div className="space-y-2">
        <Input
          label={computer ? 'Or paste the page’s address' : 'Paste the page’s address'}
          value={pasted}
          onChange={(e) => setPasted(e.target.value)}
          placeholder="https://"
          error={pasted.trim() && !pastedValue ? "That address doesn't have what this needs." : undefined}
        />
        {pastedValue && (
          <Button size="sm" loading={saving === pastedValue} disabled={saving !== null} onClick={() => void pick(pastedValue)}>
            Use {pastedValue}
          </Button>
        )}
      </div>
    </div>
  )
}

/**
 * useMyComputer is the desktop agent to ask for tab matches: this computer's when the server can
 * tell it is the caller's, else the caller's only online computer; null when there is none to ask.
 */
function useMyComputer(userId: number | undefined): string | null {
  const nodesQ = useQuery({ queryKey: ['nodes', 'liveness'], queryFn: getNodeLiveness, staleTime: 30_000 })
  const hereQ = useQuery({ queryKey: ['addons', 'this-device'], queryFn: () => getThisDevice(), retry: false, staleTime: 30_000 })
  if (userId === undefined) return null
  const mine = (nodesQ.data ?? []).filter((n) => n.node_kind === 'desktop' && n.owner_user_id === userId && n.online)
  const here = hereQ.data?.node_id
  if (here && mine.some((n) => n.node_id === here)) return here
  return mine.length === 1 ? mine[0].node_id : null
}

import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { ChevronLeft, Database, Plus } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { isAxiosError } from 'axios'
import { listChatNodes } from '@/api/chat'
import {
  browseRecords,
  createRecord,
  deleteRecord,
  getRecord,
  getSchema,
  listCommands,
  updateRecord,
  type BrowsedRecord,
  type CommandSchema,
  type FieldSpec,
} from '@/api/commandData'
import RecordField from '@/components/nodes/RecordField'
import { canEdit } from '@/lib/recordFields'
import { Banner, Button, Card, Dialog, EmptyState, ErrorState, List, PageHeader, Select, SkeletonList } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'

/** why says what a failed node round trip means, as mobile does. */
function why(err: unknown, fallback: string): string {
  const status = isAxiosError(err) ? err.response?.status : undefined
  if (status === 504) return 'The node didn’t answer. It may be offline.'
  if (status === 503) return 'The server can’t reach its nodes right now.'
  return errorMessage(err, fallback)
}

/** commandLabel is "shopping_list" as "Shopping list". */
const commandLabel = (name: string) => name.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase())

/**
 * StoredDataPage is what a node's commands keep (mobile's Stored data): pick a command, see its
 * records, and open one to read it, change its editable fields or delete it; a command that
 * supports it can add one. The node does the work; cc checks the caller is in its household.
 */
export default function StoredDataPage() {
  const { id: nodeId = '' } = useParams()
  const { activeHouseholdId: hh } = useAuth()
  const nodes = useQuery({ queryKey: ['node-liveness', 'household', hh], queryFn: () => listChatNodes(hh), enabled: !!hh, retry: false })
  const node = nodes.data?.find((n) => n.node_id === nodeId)
  const commands = useQuery({ queryKey: ['stored-data', nodeId, 'commands'], queryFn: () => listCommands(nodeId), retry: false })
  const browsable = (commands.data ?? []).filter((c) => c.mode !== 'disabled')
  const [picked, setPicked] = useState<string | null>(null)
  const command = picked ?? browsable[0]?.command_name ?? null

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <Link to="/nodes" className="inline-flex items-center gap-1 text-small text-text-muted hover:text-text">
        <ChevronLeft size={16} aria-hidden /> Nodes
      </Link>
      <PageHeader title="Stored data" description={`What ${node?.name || node?.room || 'this node'}’s commands keep.`} />
      {commands.isError ? (
        <Card>
          <ErrorState title="Couldn't ask the node" message={why(commands.error, 'Try again in a moment.')} onRetry={() => commands.refetch()} />
        </Card>
      ) : commands.isLoading ? (
        <Card flush>
          <SkeletonList rows={3} label="Asking the node" />
        </Card>
      ) : browsable.length === 0 ? (
        <Card>
          <EmptyState icon={<Database size={20} />} title="Nothing stored" description="None of this node’s commands keep data you can browse." />
        </Card>
      ) : (
        <>
          <Select
            label="Command"
            value={command ?? ''}
            onValueChange={setPicked}
            options={browsable.map((c) => ({ value: c.command_name, label: commandLabel(c.command_name) + (c.mode === 'readonly' ? ' (read-only)' : '') }))}
          />
          {command && <Records key={command} nodeId={nodeId} command={command} />}
        </>
      )}
    </div>
  )
}

type Open = { key: string | null }

function Records({ nodeId, command }: { nodeId: string; command: string }) {
  const key = ['stored-data', nodeId, command]
  const records = useQuery({ queryKey: [...key, 'records'], queryFn: () => browseRecords(nodeId, command), retry: false })
  const schema = useQuery({ queryKey: [...key, 'schema'], queryFn: () => getSchema(nodeId, command), retry: false })
  const [open, setOpen] = useState<Open | null>(null)
  const canCreate = schema.data?.mode === 'enabled' && schema.data.supports_create === true
  const rows = records.data?.records ?? []

  return (
    <>
      <Card flush className="overflow-hidden">
        <div className="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
          <h2 className="text-subheading text-text">{commandLabel(command)}</h2>
          {canCreate && (
            <Button size="sm" variant="primary" icon={<Plus size={16} aria-hidden />} onClick={() => setOpen({ key: null })}>
              Add
            </Button>
          )}
        </div>
        {records.isError ? (
          <ErrorState title="Couldn't load the records" message={why(records.error, 'Try again in a moment.')} onRetry={() => records.refetch()} />
        ) : records.isLoading ? (
          <SkeletonList rows={3} label="Loading records" />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Database size={20} />}
            title="No records"
            description={canCreate ? 'Add one here, or let the command store one.' : 'The command hasn’t stored anything yet.'}
          />
        ) : (
          <List label="Records">
            {rows.map((r) => (
              <li key={r.key}>
                <button type="button" className="w-full px-4 py-3 text-left hover:bg-surface-alt" onClick={() => setOpen({ key: r.key })}>
                  <span className="block text-body break-words text-text">{title(r)}</span>
                  {r.summary?.subtitle && <span className="block text-small break-words text-text-muted">{r.summary.subtitle}</span>}
                </button>
              </li>
            ))}
          </List>
        )}
        {records.data?.truncated && (
          <p className="border-t border-border px-4 py-3 text-small text-text-muted">
            Showing the first {rows.length} of {records.data.count}.
          </p>
        )}
      </Card>
      {open && schema.data && (
        <RecordDialog key={open.key ?? 'new'} nodeId={nodeId} command={command} recordKey={open.key} schema={schema.data} onClose={() => setOpen(null)} />
      )}
    </>
  )
}

const title = (r: BrowsedRecord) => r.summary?.title || r.key

function RecordDialog({
  nodeId,
  command,
  recordKey,
  schema,
  onClose,
}: {
  nodeId: string
  command: string
  recordKey: string | null
  schema: CommandSchema
  onClose: () => void
}) {
  const qc = useQueryClient()
  const creating = recordKey === null
  const rec = useQuery({
    queryKey: ['stored-data', nodeId, command, 'record', recordKey],
    queryFn: () => getRecord(nodeId, command, recordKey!),
    enabled: !creating,
    retry: false,
  })
  const fields = (rec.data?.schema ?? schema).fields
  const editableMode = (rec.data?.schema ?? schema).mode === 'enabled'
  const writable = (f: FieldSpec) =>
    editableMode && canEdit(f) && (creating ? f.editable !== false || f.create_only === true : f.editable !== false && !f.create_only)
  const shownFields = creating ? fields.filter(writable) : fields
  const [draft, setDraft] = useState<Record<string, unknown>>({})
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const saved = rec.data?.record ?? {}
  const value = (f: FieldSpec) => (f.name in draft ? draft[f.name] : saved[f.name])
  const changed = Object.keys(draft).length > 0
  const missing = creating && fields.some((f) => writable(f) && f.required && (value(f) === undefined || value(f) === null || value(f) === ''))
  const refresh = () => qc.invalidateQueries({ queryKey: ['stored-data', nodeId, command] })

  async function save() {
    setBusy(true)
    try {
      if (creating) await createRecord(nodeId, command, draft)
      else await updateRecord(nodeId, command, recordKey!, draft)
      await refresh()
      toast.success(creating ? 'Added' : 'Saved')
      onClose()
    } catch (err) {
      toast.error(why(err, 'The node didn’t save it'))
      setBusy(false)
    }
  }

  async function remove() {
    setBusy(true)
    try {
      await deleteRecord(nodeId, command, recordKey!)
      await refresh()
      toast('Deleted')
      onClose()
    } catch (err) {
      toast.error(why(err, 'The node didn’t delete it'))
      setBusy(false)
    }
  }

  const anyWritable = shownFields.some(writable)
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      dismissible={!busy}
      title={creating ? `Add to ${commandLabel(command)}` : commandLabel(command)}
      footer={
        <>
          {!creating && editableMode && !confirming && (
            <Button variant="ghost" className="mr-auto" disabled={busy || !rec.data} onClick={() => setConfirming(true)}>
              Delete
            </Button>
          )}
          <Button disabled={busy} onClick={onClose}>
            {anyWritable ? 'Cancel' : 'Close'}
          </Button>
          {anyWritable && (
            <Button variant="primary" type="submit" form="record-form" loading={busy} disabled={!changed || missing}>
              {creating ? 'Add' : 'Save'}
            </Button>
          )}
        </>
      }
    >
      {confirming && (
        <Banner
          role="group"
          aria-label="Delete this record"
          tone="danger"
          className="mb-4"
          actions={
            <>
              <Button size="sm" variant="danger" loading={busy} onClick={() => void remove()}>
                Delete
              </Button>
              <Button size="sm" disabled={busy} onClick={() => setConfirming(false)}>
                Keep it
              </Button>
            </>
          }
        >
          <p>Delete this record from the node? This can’t be undone.</p>
        </Banner>
      )}
      {rec.isError ? (
        <ErrorState title="Couldn't load the record" message={why(rec.error, 'Try again in a moment.')} onRetry={() => rec.refetch()} />
      ) : !creating && rec.isLoading ? (
        <SkeletonList rows={3} label="Loading the record" />
      ) : (
        <form
          id="record-form"
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (changed && !missing) void save()
          }}
        >
          {shownFields.map((f) => (
            <RecordField
              key={f.name}
              f={f}
              value={value(f)}
              display={saved[`${f.name}_display`]}
              readOnly={!writable(f)}
              onChange={(v) => setDraft((d) => ({ ...d, [f.name]: v }))}
            />
          ))}
        </form>
      )}
    </Dialog>
  )
}

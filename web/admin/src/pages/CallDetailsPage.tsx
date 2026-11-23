import { useState } from 'react'
import { IdCard, Lock, Pencil, Plus, Trash2 } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { getCallContext, putCallContext, type CallCatalog, type CallContext, type CallField, type CallTier } from '@/api/callContext'
import { Badge, Button, Card, ChoiceGroup, Dialog, EmptyState, ErrorState, Input, List, PageHeader, Select, SkeletonList } from '@/components/ui'
import { errorMessage } from '@/lib/errors'

const KEY = ['call-context']

/**
 * CallDetailsPage is what Jarvis may tell a business when it calls for you (mobile's Call
 * details): your name, callback number, insurance and the like, each saying whether Jarvis may
 * volunteer it or only answer when asked. They are yours, not the household's.
 */
export default function CallDetailsPage() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: KEY, queryFn: getCallContext, retry: false })
  const [editing, setEditing] = useState<{ index: number | null } | null>(null)
  const [removing, setRemoving] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const fields = q.data?.fields ?? []
  const catalog = q.data?.catalog
  const category = (v: string) => catalog?.categories.find((c) => c.value === v)?.label ?? v

  async function remove() {
    if (removing === null) return
    setBusy(true)
    try {
      qc.setQueryData<CallContext>(KEY, await putCallContext(fields.filter((_, i) => i !== removing)))
      toast(`Removed ${fields[removing].label}`)
      setRemoving(null)
    } catch (err) {
      toast.error(errorMessage(err, 'Could not remove it'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        title="Call details"
        description="What Jarvis can tell a business when it calls for you. Only you see these."
        actions={
          catalog && (
            <Button size="sm" variant="primary" icon={<Plus size={16} aria-hidden />} onClick={() => setEditing({ index: null })}>
              Add detail
            </Button>
          )
        }
      />
      <Card flush className="overflow-hidden">
        {q.isError ? (
          <ErrorState
            title="Couldn't load your call details"
            message={errorMessage(q.error, 'Check your connection and try again.')}
            onRetry={() => q.refetch()}
          />
        ) : q.isLoading ? (
          <SkeletonList rows={3} label="Loading your call details" />
        ) : fields.length === 0 ? (
          <EmptyState
            icon={<IdCard size={20} />}
            title="No details yet"
            description="Save your name, callback number or insurance once, and Jarvis can use them on a call instead of asking you. Mark anything private “Only if they ask”."
          />
        ) : (
          <List label="Call details">
            {fields.map((f, i) => (
              <li key={f.key ?? f.label} className="flex items-start gap-3 px-4 py-3">
                <span className="min-w-0 flex-1">
                  <span className="block text-small text-text-muted">{f.label}</span>
                  <span className="block text-body break-words text-text">{f.value}</span>
                  <span className="mt-1.5 flex flex-wrap gap-1.5">
                    <Badge>{category(f.category)}</Badge>
                    {f.tier === 'if_asked' && (
                      <Badge>
                        <Lock size={12} aria-hidden /> Only if they ask
                      </Badge>
                    )}
                  </span>
                </span>
                <span className="flex shrink-0 gap-1">
                  <Button size="icon" variant="ghost" aria-label={`Edit ${f.label}`} onClick={() => setEditing({ index: i })}>
                    <Pencil size={16} aria-hidden />
                  </Button>
                  <Button size="icon" variant="ghost" aria-label={`Remove ${f.label}`} onClick={() => setRemoving(i)}>
                    <Trash2 size={16} aria-hidden />
                  </Button>
                </span>
              </li>
            ))}
          </List>
        )}
      </Card>

      {editing && catalog && (
        <DetailDialog key={editing.index ?? 'new'} fields={fields} catalog={catalog} index={editing.index} onClose={() => setEditing(null)} />
      )}

      <Dialog
        open={removing !== null}
        onClose={() => !busy && setRemoving(null)}
        dismissible={!busy}
        title={removing !== null ? `Remove ${fields[removing]?.label}?` : 'Remove?'}
        description="Jarvis will ask you for it again the next time a call needs it."
        footer={
          <>
            <Button disabled={busy} onClick={() => setRemoving(null)}>
              Keep it
            </Button>
            <Button variant="danger" loading={busy} onClick={() => void remove()}>
              Remove
            </Button>
          </>
        }
      />
    </div>
  )
}

function DetailDialog({ fields, catalog, index, onClose }: { fields: CallField[]; catalog: CallCatalog; index: number | null; onClose: () => void }) {
  const qc = useQueryClient()
  const existing = index === null ? undefined : fields[index]
  const [key, setKey] = useState(existing?.key)
  const [label, setLabel] = useState(existing?.label ?? '')
  const [value, setValue] = useState(existing?.value ?? '')
  const [category, setCategory] = useState(existing?.category ?? 'general')
  const [tier, setTier] = useState<CallTier>(existing?.tier ?? 'if_asked')
  const [saving, setSaving] = useState(false)
  const used = new Set(fields.map((f) => f.key).filter(Boolean))
  const presets = catalog.well_known.filter((w) => !used.has(w.key))
  // Same-named details slug to the same key and the server keeps one: catch it here.
  const clash = fields.some((f, i) => i !== index && f.label.trim().toLowerCase() === label.trim().toLowerCase())
  const ready = label.trim() !== '' && value.trim() !== '' && !clash

  async function save() {
    setSaving(true)
    const edited: CallField = { ...(key ? { key } : {}), label: label.trim(), value: value.trim(), category, tier }
    try {
      const next = index === null ? [...fields, edited] : fields.map((f, i) => (i === index ? edited : f))
      qc.setQueryData<CallContext>(KEY, await putCallContext(next))
      toast.success(index === null ? `Added ${edited.label}` : 'Saved')
      onClose()
    } catch (err) {
      toast.error(errorMessage(err, 'Could not save it'))
      setSaving(false)
    }
  }

  return (
    <Dialog
      open
      onClose={() => !saving && onClose()}
      dismissible={!saving}
      title={existing ? `Edit ${existing.label}` : 'Add a detail'}
      footer={
        <>
          <Button disabled={saving} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="call-detail-form" loading={saving} disabled={!ready}>
            Save
          </Button>
        </>
      }
    >
      <form
        id="call-detail-form"
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (ready) void save()
        }}
      >
        {!existing && presets.length > 0 && (
          <Select
            label="Start from a common detail"
            placeholder="Or write your own below"
            value={key ?? ''}
            options={presets.map((p) => ({ value: p.key, label: p.label }))}
            onValueChange={(k) => {
              const p = presets.find((x) => x.key === k)
              setKey(p?.key)
              if (p) {
                setLabel(p.label)
                setCategory(p.category)
                setTier(p.tier)
              }
            }}
          />
        )}
        <Input
          label="Name"
          value={label}
          maxLength={100}
          placeholder="e.g. Rewards number"
          error={clash ? 'You already have a detail with that name' : undefined}
          onChange={(e) => {
            setLabel(e.target.value)
            // Renamed, it's your own detail rather than the common one.
            setKey(undefined)
          }}
        />
        <Input label="Value" value={value} maxLength={300} placeholder="e.g. 9982-4471" onChange={(e) => setValue(e.target.value)} />
        <Select
          label="Category"
          hint="Tags the detail for later; today every detail is available on every call."
          value={category}
          options={catalog.categories}
          onValueChange={setCategory}
        />
        <div className="space-y-1">
          <ChoiceGroup legend="When can Jarvis say it?" value={tier} onChange={setTier} options={catalog.tiers} />
          <p className="text-small text-text-muted">Use “Only if they ask” for anything sensitive: Jarvis keeps it back unless the other side asks.</p>
        </div>
      </form>
    </Dialog>
  )
}

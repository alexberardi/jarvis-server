import { useState } from 'react'
import { Brain, Lock, Pencil, Pin, Plus, Trash2 } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { createMemory, deleteMemory, listMemories, updateMemory, type Memory, type MemoryScope } from '@/api/memories'
import { Badge, Button, Card, ChoiceGroup, Dialog, EmptyState, ErrorState, List, PageHeader, SkeletonList, Switch } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

const CATEGORIES = ['general', 'preference', 'fact', 'note'] as const
type Category = (typeof CATEGORIES)[number]

/** A background agent's memory (calendar, news, weather): read-only, kept up to date for you. */
const isAgent = (m: Memory) => m.source === 'agent' || m.category === 'agent_context'

type Editing = { memory: Memory | null }

/**
 * MemoriesPage is what Jarvis remembers (mobile's Memories screens): yours, and for power users
 * and admins the household's shared ones. Each can be edited or forgotten unless cc says it's
 * read-only (a background agent's, or another member's).
 */
export default function MemoriesPage() {
  const { activeHouseholdId: hh, households } = useAuth()
  const qc = useQueryClient()
  const key = ['memories', hh]
  const list = useQuery({ queryKey: key, queryFn: () => listMemories(hh), retry: false })
  const role = households.find((h) => h.id === hh)?.role ?? 'member'
  const elevated = role === 'owner' || role === 'admin' || role === 'power_user'
  const [editing, setEditing] = useState<Editing | null>(null)
  const [forgetting, setForgetting] = useState<Memory | null>(null)
  const [busy, setBusy] = useState(false)

  const all = list.data ?? []
  const mine = all.filter((m) => m.user_id !== null)
  const household = elevated ? all.filter((m) => m.user_id === null) : []

  async function forget() {
    if (!forgetting) return
    setBusy(true)
    try {
      await deleteMemory(hh, forgetting.id)
      await qc.invalidateQueries({ queryKey: key })
      toast('Forgotten')
      setForgetting(null)
    } catch (err) {
      toast.error(errorMessage(err, 'Could not forget it'))
    } finally {
      setBusy(false)
    }
  }

  const section = (title: string, rows: Memory[]) => (
    <section aria-label={title}>
      <h2 className="px-4 pt-4 pb-1 text-small font-semibold text-text-muted">{title}</h2>
      <List label={title}>
        {rows.map((m) => (
          <li key={m.id} className="flex items-start gap-3 px-4 py-3">
            <span className="min-w-0 flex-1">
              <span className="block text-body text-pretty whitespace-pre-line text-text">{m.content}</span>
              <span className="mt-1.5 flex flex-wrap gap-1.5">
                <Badge>{m.category.replace(/_/g, ' ')}</Badge>
                {m.is_pinned && (
                  <Badge tone="primary">
                    <Pin size={12} aria-hidden /> Pinned
                  </Badge>
                )}
                {isAgent(m) && <Badge>Kept by an agent</Badge>}
                {!m.editable && (
                  <Badge>
                    <Lock size={12} aria-hidden /> Read-only
                  </Badge>
                )}
              </span>
            </span>
            {m.editable && (
              <span className="flex shrink-0 gap-1">
                <Button size="icon" variant="ghost" aria-label={`Edit: ${m.content.slice(0, 40)}`} onClick={() => setEditing({ memory: m })}>
                  <Pencil size={16} aria-hidden />
                </Button>
                <Button size="icon" variant="ghost" aria-label={`Forget: ${m.content.slice(0, 40)}`} onClick={() => setForgetting(m)}>
                  <Trash2 size={16} aria-hidden />
                </Button>
              </span>
            )}
          </li>
        ))}
      </List>
    </section>
  )

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        title="Memories"
        description="What Jarvis remembers about you and uses when it answers."
        actions={
          <Button size="sm" variant="primary" icon={<Plus size={16} aria-hidden />} onClick={() => setEditing({ memory: null })}>
            New memory
          </Button>
        }
      />
      <Card flush className="overflow-hidden">
        {list.isError ? (
          <ErrorState title="Couldn't load memories" message={errorMessage(list.error, 'Check your connection and try again.')} onRetry={() => list.refetch()} />
        ) : list.isLoading ? (
          <SkeletonList rows={3} label="Loading memories" />
        ) : mine.length + household.length === 0 ? (
          <EmptyState
            icon={<Brain size={20} />}
            title="Nothing remembered yet"
            description="Tell Jarvis “remember that I like oat milk in my coffee”, or add one here."
          />
        ) : (
          <div className="divide-y divide-border">
            {mine.length > 0 && section('Yours', mine)}
            {household.length > 0 && section('Household', household)}
          </div>
        )}
      </Card>

      {editing && <MemoryDialog key={editing.memory?.id ?? 'new'} memory={editing.memory} canShare={elevated} onClose={() => setEditing(null)} />}

      <Dialog
        open={forgetting !== null}
        onClose={() => !busy && setForgetting(null)}
        dismissible={!busy}
        title="Forget this?"
        description={forgetting ? `Jarvis stops using “${forgetting.content.slice(0, 120)}${forgetting.content.length > 120 ? '…' : ''}”.` : undefined}
        footer={
          <>
            <Button disabled={busy} onClick={() => setForgetting(null)}>
              Keep it
            </Button>
            <Button variant="danger" loading={busy} onClick={() => void forget()}>
              Forget
            </Button>
          </>
        }
      />
    </div>
  )
}

function MemoryDialog({ memory, canShare, onClose }: { memory: Memory | null; canShare: boolean; onClose: () => void }) {
  const { activeHouseholdId: hh } = useAuth()
  const qc = useQueryClient()
  const [content, setContent] = useState(memory?.content ?? '')
  const [category, setCategory] = useState<Category>((CATEGORIES as readonly string[]).includes(memory?.category ?? '') ? (memory!.category as Category) : 'general')
  const [pinned, setPinned] = useState(memory?.is_pinned ?? false)
  const [scope, setScope] = useState<MemoryScope>('user')
  const [saving, setSaving] = useState(false)

  async function save() {
    setSaving(true)
    try {
      const body = { content: content.trim(), category, is_pinned: pinned }
      if (memory) await updateMemory(hh, memory.id, body)
      else await createMemory(hh, { ...body, scope })
      await qc.invalidateQueries({ queryKey: ['memories', hh] })
      toast.success(memory ? 'Memory saved' : 'Jarvis will remember that')
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
      title={memory ? 'Edit memory' : 'New memory'}
      footer={
        <>
          <Button disabled={saving} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="memory-form" loading={saving} disabled={!content.trim()}>
            Save
          </Button>
        </>
      }
    >
      <form
        id="memory-form"
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (content.trim()) void save()
        }}
      >
        <div className="space-y-1.5">
          <label htmlFor="memory-content" className="text-small font-medium text-text">
            What should Jarvis remember?
          </label>
          <textarea
            id="memory-content"
            rows={4}
            maxLength={2000}
            className={cn(fieldControl, 'h-auto py-2')}
            value={content}
            placeholder="e.g. likes oat milk in coffee"
            onChange={(e) => setContent(e.target.value)}
          />
        </div>
        <ChoiceGroup legend="Kind" value={category} onChange={setCategory} options={CATEGORIES.map((c) => ({ value: c, label: c[0].toUpperCase() + c.slice(1) }))} />
        {!memory && canShare && (
          <ChoiceGroup
            legend="Whose"
            value={scope}
            onChange={setScope}
            options={[
              { value: 'user', label: 'Just me' },
              { value: 'household', label: 'Household' },
            ]}
          />
        )}
        <Switch checked={pinned} onCheckedChange={setPinned} label="Pin it" description="Pinned memories are always included when Jarvis answers." />
      </form>
    </Dialog>
  )
}

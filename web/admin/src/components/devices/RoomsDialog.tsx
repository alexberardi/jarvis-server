import { useState } from 'react'
import { Pencil, Plus, Trash2 } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { createRoom, deleteRoom, updateRoom, type DeviceRoom } from '@/api/devices'
import { Banner, Button, Dialog, Input, Select } from '@/components/ui'
import { errorMessage } from '@/lib/errors'

/** path is "Upstairs › Bedroom" for a nested room: its ancestors, top first. */
function path(room: DeviceRoom, byId: Map<string, DeviceRoom>): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (
    let r = room.parent_room_id ? byId.get(room.parent_room_id) : undefined;
    r && !seen.has(r.id);
    r = r.parent_room_id ? byId.get(r.parent_room_id) : undefined
  ) {
    seen.add(r.id)
    out.unshift(r.name)
  }
  return out
}

/** below is the room and every room inside it: none of them can be its parent. */
function below(id: string, rooms: DeviceRoom[]): Set<string> {
  const out = new Set([id])
  for (let grew = true; grew;) {
    grew = false
    for (const r of rooms) {
      if (r.parent_room_id && out.has(r.parent_room_id) && !out.has(r.id)) {
        out.add(r.id)
        grew = true
      }
    }
  }
  return out
}

/** deleteText says what goes with a room: cc deletes the rooms inside it too; devices stay, unassigned. */
function deleteText(room: DeviceRoom, rooms: DeviceRoom[]): string {
  const ids = below(room.id, rooms)
  const inner = rooms.filter((r) => r.id !== room.id && ids.has(r.id))
  const devices = rooms.filter((r) => ids.has(r.id)).reduce((n, r) => n + (r.device_count ?? 0), 0)
  const also = inner.length > 0 ? ` and the room${inner.length === 1 ? '' : 's'} inside it (${inner.map((r) => r.name).join(', ')})` : ''
  const left = devices > 0 ? ` Its ${devices} device${devices === 1 ? '' : 's'} stay, without a room.` : ''
  return `Delete ${room.name}${also}?${left}`
}

type Editing = { room: DeviceRoom | null; name: string; parent: string }

/**
 * RoomsDialog is the household's rooms (mobile's Rooms): add one, rename it or put it inside
 * another, and delete it with the rooms inside it (their devices stay, unassigned).
 */
export default function RoomsDialog({ household, rooms, onClose }: { household: string; rooms: DeviceRoom[]; onClose: () => void }) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Editing | null>(null)
  const [removing, setRemoving] = useState<DeviceRoom | null>(null)
  const [busy, setBusy] = useState(false)
  const byId = new Map(rooms.map((r) => [r.id, r]))
  const sorted = [...rooms].sort((a, b) => [...path(a, byId), a.name].join('\u0000').localeCompare([...path(b, byId), b.name].join('\u0000')))
  const changed = () => qc.invalidateQueries({ queryKey: ['devices', household] })

  async function save() {
    if (!editing) return
    setBusy(true)
    const body = { name: editing.name.trim(), parent_room_id: editing.parent || null }
    try {
      if (editing.room) await updateRoom(household, editing.room.id, body)
      else await createRoom(household, body)
      await changed()
      toast.success(editing.room ? 'Saved' : `Added ${body.name}`)
      setEditing(null)
    } catch (err) {
      toast.error(errorMessage(err, 'Could not save the room'))
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    if (!removing) return
    setBusy(true)
    try {
      await deleteRoom(household, removing.id)
      await changed()
      toast(`Deleted ${removing.name}`)
      setRemoving(null)
    } catch (err) {
      toast.error(errorMessage(err, 'Could not delete the room'))
    } finally {
      setBusy(false)
    }
  }

  const parents = editing ? sorted.filter((r) => !(editing.room && below(editing.room.id, rooms).has(r.id))) : []

  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      dismissible={!busy}
      title="Rooms"
      description="Rooms let you say “the kitchen lights”. A room can sit inside another, like a bedroom upstairs."
      footer={
        !editing && (
          <Button variant="primary" icon={<Plus size={16} aria-hidden />} onClick={() => setEditing({ room: null, name: '', parent: '' })}>
            Add room
          </Button>
        )
      }
    >
      {editing ? (
        <form
          className="space-y-4"
          aria-label={editing.room ? `Edit ${editing.room.name}` : 'Add a room'}
          onSubmit={(e) => {
            e.preventDefault()
            if (editing.name.trim()) void save()
          }}
        >
          <Input label="Name" value={editing.name} placeholder="e.g. Kitchen" autoFocus onChange={(e) => setEditing({ ...editing, name: e.target.value })} />
          <Select
            label="Inside"
            value={editing.parent}
            onValueChange={(v) => setEditing({ ...editing, parent: v })}
            options={[
              { value: '', label: 'Nothing (a top-level room)' },
              ...parents.map((r) => ({ value: r.id, label: [...path(r, byId), r.name].join(' › ') })),
            ]}
          />
          <div className="flex justify-end gap-2">
            <Button disabled={busy} onClick={() => setEditing(null)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" loading={busy} disabled={!editing.name.trim()}>
              {editing.room ? 'Save' : 'Add'}
            </Button>
          </div>
        </form>
      ) : sorted.length === 0 ? (
        <p className="text-body text-text-muted">No rooms yet. Add one, then put devices in it from their details.</p>
      ) : (
        <ul className="divide-y divide-border" aria-label="Rooms">
          {sorted.map((r) => {
            const where = path(r, byId)
            const n = r.device_count ?? 0
            return (
              <li key={r.id} className="py-2">
                {removing?.id === r.id ? (
                  <Banner
                    role="group"
                    aria-label={`Delete ${r.name}`}
                    tone="danger"
                    actions={
                      <>
                        <Button size="sm" variant="danger" loading={busy} onClick={() => void remove()}>
                          Delete
                        </Button>
                        <Button size="sm" disabled={busy} onClick={() => setRemoving(null)}>
                          Cancel
                        </Button>
                      </>
                    }
                  >
                    <p>{deleteText(r, sorted)}</p>
                  </Banner>
                ) : (
                  <div className="flex items-center gap-2">
                    <span className="min-w-0 flex-1" style={{ paddingLeft: `${where.length * 1}rem` }}>
                      <span className="block text-body break-words text-text">{r.name}</span>
                      <span className="block text-small text-text-muted">
                        {where.length > 0 && `In ${where.join(' › ')} · `}
                        {n} device{n === 1 ? '' : 's'}
                      </span>
                    </span>
                    <Button
                      size="icon"
                      variant="ghost"
                      aria-label={`Edit ${r.name}`}
                      onClick={() => setEditing({ room: r, name: r.name, parent: r.parent_room_id ?? '' })}
                    >
                      <Pencil size={16} aria-hidden />
                    </Button>
                    <Button size="icon" variant="ghost" aria-label={`Delete ${r.name}`} onClick={() => setRemoving(r)}>
                      <Trash2 size={16} aria-hidden />
                    </Button>
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Dialog>
  )
}

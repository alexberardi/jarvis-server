import { useState } from 'react'
import { BookUser, Pencil, PhoneOff, Plus, Trash2 } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  contactRejection,
  createPhoneContact,
  deletePhoneContact,
  formatPhone,
  listPhoneContacts,
  SOURCE_LABEL,
  updatePhoneContact,
  type PhoneContact,
} from '@/api/phoneContacts'
import { Badge, Button, Card, Dialog, EmptyState, ErrorState, Input, List, PageHeader, SkeletonList, Switch } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { formatServerTime } from '@/lib/time'

type Editing = { contact: PhoneContact | null }

/**
 * PhonebookPage is the businesses Jarvis may call for the household (mobile's Phonebook
 * screens): added here, saved after a call, or found by a search. "Do not call" makes Jarvis
 * refuse to dial one.
 */
export default function PhonebookPage() {
  const { activeHouseholdId: hh } = useAuth()
  const qc = useQueryClient()
  const key = ['phone-contacts', hh]
  const list = useQuery({ queryKey: key, queryFn: () => listPhoneContacts(hh), retry: false })
  const [editing, setEditing] = useState<Editing | null>(null)
  const [removing, setRemoving] = useState<PhoneContact | null>(null)
  const [busy, setBusy] = useState(false)
  const rows = [...(list.data ?? [])].sort((a, b) => a.name.localeCompare(b.name))

  async function remove() {
    if (!removing) return
    setBusy(true)
    try {
      await deletePhoneContact(hh, removing.id)
      await qc.invalidateQueries({ queryKey: key })
      toast(`Removed ${removing.name}`)
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
        title="Phonebook"
        description="Businesses Jarvis can call for you."
        actions={
          <Button size="sm" variant="primary" icon={<Plus size={16} aria-hidden />} onClick={() => setEditing({ contact: null })}>
            Add business
          </Button>
        }
      />
      <Card flush className="overflow-hidden">
        {list.isError ? (
          <ErrorState title="Couldn't load the phonebook" message={errorMessage(list.error, 'Check your connection and try again.')} onRetry={() => list.refetch()} />
        ) : list.isLoading ? (
          <SkeletonList rows={3} label="Loading the phonebook" />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<BookUser size={20} />}
            title="No businesses yet"
            description="Add one here, or ask Jarvis to call a place: it can look up the number and save it after the call."
          />
        ) : (
          <List label="Businesses">
            {rows.map((c) => (
              <li key={c.id} className="flex items-start gap-3 px-4 py-3">
                <span className="min-w-0 flex-1">
                  <span className="block text-subheading text-pretty text-text">{c.name}</span>
                  <span className="block text-small text-text-muted tabular-nums">{formatPhone(c.number)}</span>
                  {c.address && <span className="block text-small text-pretty text-text-muted">{c.address}</span>}
                  <span className="mt-1.5 flex flex-wrap gap-1.5">
                    {c.do_not_call && (
                      <Badge tone="danger">
                        <PhoneOff size={12} aria-hidden /> Do not call
                      </Badge>
                    )}
                    {/* Only the sources that say something: most are added by hand. */}
                    {c.source !== 'manual' && <Badge>{SOURCE_LABEL[c.source] ?? c.source}</Badge>}
                  </span>
                </span>
                <span className="flex shrink-0 gap-1">
                  <Button size="icon" variant="ghost" aria-label={`Edit ${c.name}`} onClick={() => setEditing({ contact: c })}>
                    <Pencil size={16} aria-hidden />
                  </Button>
                  <Button size="icon" variant="ghost" aria-label={`Remove ${c.name}`} onClick={() => setRemoving(c)}>
                    <Trash2 size={16} aria-hidden />
                  </Button>
                </span>
              </li>
            ))}
          </List>
        )}
      </Card>

      {editing && <ContactDialog key={editing.contact?.id ?? 'new'} contact={editing.contact} onClose={() => setEditing(null)} />}

      <Dialog
        open={removing !== null}
        onClose={() => !busy && setRemoving(null)}
        dismissible={!busy}
        title="Remove from the phonebook?"
        description={removing ? `Jarvis will need to look up ${removing.name}’s number again to call it.` : undefined}
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

function ContactDialog({ contact, onClose }: { contact: PhoneContact | null; onClose: () => void }) {
  const { activeHouseholdId: hh } = useAuth()
  const qc = useQueryClient()
  const [name, setName] = useState(contact?.name ?? '')
  const [number, setNumber] = useState(contact?.number ?? '')
  const [address, setAddress] = useState(contact?.address ?? '')
  const [notes, setNotes] = useState(contact?.notes ?? '')
  const [dnc, setDnc] = useState(contact?.do_not_call ?? false)
  const [rejected, setRejected] = useState<{ field: 'name' | 'number'; message: string } | null>(null)
  const [saving, setSaving] = useState(false)
  const ready = name.trim() !== '' && number.trim() !== ''

  async function save() {
    setSaving(true)
    setRejected(null)
    try {
      const body = { name: name.trim(), number: number.trim(), address: address.trim(), notes: notes.trim() }
      if (contact) await updatePhoneContact(hh, contact.id, { ...body, do_not_call: dnc })
      else await createPhoneContact(hh, body)
      await qc.invalidateQueries({ queryKey: ['phone-contacts', hh] })
      toast.success(contact ? 'Saved' : `Added ${body.name}`)
      onClose()
    } catch (err) {
      const r = contactRejection(err)
      if (r) setRejected(r)
      else toast.error(errorMessage(err, 'Could not save it'))
      setSaving(false)
    }
  }

  return (
    <Dialog
      open
      onClose={() => !saving && onClose()}
      dismissible={!saving}
      title={contact ? `Edit ${contact.name}` : 'Add a business'}
      footer={
        <>
          <Button disabled={saving} onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="contact-form" loading={saving} disabled={!ready}>
            Save
          </Button>
        </>
      }
    >
      <form
        id="contact-form"
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (ready) void save()
        }}
      >
        <Input label="Name" value={name} placeholder="e.g. Tony’s Pizzeria" onChange={(e) => setName(e.target.value)} error={rejected?.field === 'name' ? rejected.message : undefined} />
        <Input
          label="Phone number"
          type="tel"
          inputMode="tel"
          autoComplete="off"
          value={number}
          placeholder="+1 555 123 4567"
          onChange={(e) => setNumber(e.target.value)}
          error={rejected?.field === 'number' ? rejected.message : undefined}
          hint={contact ? `Dials as ${formatPhone(contact.number)}${contact.line_type ? ` · ${contact.line_type}` : ''}` : undefined}
        />
        <Input label="Address (optional)" value={address} onChange={(e) => setAddress(e.target.value)} />
        <Input label="Notes (optional)" value={notes} placeholder="e.g. usual order, ask for Maria" onChange={(e) => setNotes(e.target.value)} />
        {contact && (
          <>
            <Switch checked={dnc} onCheckedChange={setDnc} label="Do not call" description="Jarvis will refuse to call this business." />
            <p className="text-small text-text-muted">
              {SOURCE_LABEL[contact.source] ?? contact.source} · added {formatServerTime(contact.created_at, { dateStyle: 'medium' })}
            </p>
          </>
        )}
      </form>
    </Dialog>
  )
}

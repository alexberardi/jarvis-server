import { useState } from 'react'
import { Check, Copy, LogOut, Plus, UserMinus } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { isAxiosError } from 'axios'
import {
  createInvite,
  joinHousehold,
  leaveHousehold,
  listInvites,
  listMembers,
  removeMember,
  renameHousehold,
  revokeInvite,
  ROLE_LABEL,
  setMemberRole,
  type HouseholdRole,
  type Invite,
  type Member,
} from '@/api/household'
import HouseholdSettings from '@/components/account/HouseholdSettings'
import SignalAutomations from '@/components/account/SignalAutomations'
import { PlaceCard, VoiceCard } from '@/components/account/HouseholdVoiceAndPlace'
import { Badge, Button, Card, CardHeader, Dialog, ErrorState, Input, PageHeader, Select, SkeletonList } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { nameOf } from '@/lib/session'
import { formatServerTime, parseServerTime } from '@/lib/time'

const ROLE_OPTIONS = (['member', 'power_user', 'admin'] as const).map((r) => ({
  value: r,
  label: ROLE_LABEL[r],
}))

/** The detail auth sends with a refusal ("You are the last admin"), else the fallback. */
const refusal = (err: unknown, fallback: string) => {
  const d = isAxiosError(err) ? (err.response?.data as { detail?: unknown } | undefined)?.detail : undefined
  return typeof d === 'string' && d.trim() ? d : errorMessage(err, fallback)
}

/**
 * HouseholdPage is the active household's people and settings (mobile's Household settings):
 * its name, members and their roles, invite codes, how Jarvis talks, where the household is,
 * the feature switches, joining another household and leaving this one. Location and presence
 * detection stay on the phone (they come from its GPS). Auth and cc enforce who may do what;
 * the page only hides controls the caller's role can't use.
 */
export default function HouseholdPage() {
  const { activeHouseholdId: hh, households, state } = useAuth()
  const me = households.find((h) => h.id === hh)
  const role = me?.role ?? 'member'
  const isAdmin = role === 'admin' || role === 'owner'
  const canInvite = isAdmin || role === 'power_user'

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <PageHeader title="Household" description={me ? `${me.name} · you’re ${ROLE_LABEL[role]?.toLowerCase() ?? role}` : undefined} />
      {isAdmin && me && <NameCard id={hh} name={me.name} />}
      <MembersCard id={hh} isAdmin={isAdmin} myId={state.user?.id} />
      {/* As on mobile: codes let people in, so only those who may invite see them. */}
      {canInvite && <InvitesCard id={hh} isAdmin={isAdmin} />}
      <VoiceCard hh={hh} canEdit={isAdmin} />
      <PlaceCard hh={hh} canEdit={isAdmin} />
      <HouseholdSettings />
      <SignalAutomations hh={hh} canEdit={isAdmin} />
      <JoinCard />
      {households.length > 1 && me && <LeaveCard id={hh} name={me.name} />}
    </div>
  )
}

function NameCard({ id, name }: { id: string; name: string }) {
  const { reloadHouseholds } = useAuth()
  const [value, setValue] = useState(name)
  const [saving, setSaving] = useState(false)
  async function save() {
    setSaving(true)
    try {
      await renameHousehold(id, value.trim())
      await reloadHouseholds()
      toast.success('Renamed')
    } catch (err) {
      toast.error(refusal(err, 'Could not rename it'))
    } finally {
      setSaving(false)
    }
  }
  return (
    <Card as="section" aria-labelledby="household-name">
      <CardHeader id="household-name" title="Name" />
      <form
        className="flex items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <Input label="Household name" value={value} onChange={(e) => setValue(e.target.value)} wrapperClassName="flex-1" />
        <Button type="submit" loading={saving} disabled={!value.trim() || value.trim() === name}>
          Save
        </Button>
      </form>
    </Card>
  )
}

function MembersCard({ id, isAdmin, myId }: { id: string; isAdmin: boolean; myId?: number }) {
  const qc = useQueryClient()
  const key = ['household', id, 'members']
  const list = useQuery({
    queryKey: key,
    queryFn: () => listMembers(id),
    retry: false,
  })
  const [removing, setRemoving] = useState<Member | null>(null)
  const [busy, setBusy] = useState(false)

  async function changeRole(m: Member, role: HouseholdRole) {
    try {
      await setMemberRole(id, m.user_id, role)
      await qc.invalidateQueries({ queryKey: key })
      toast.success(`${nameOf(m)} is now ${ROLE_LABEL[role].toLowerCase()}`)
    } catch (err) {
      toast.error(refusal(err, 'Could not change the role'))
    }
  }

  async function remove() {
    if (!removing) return
    setBusy(true)
    try {
      await removeMember(id, removing.user_id)
      await qc.invalidateQueries({ queryKey: key })
      toast(`Removed ${nameOf(removing)}`)
      setRemoving(null)
    } catch (err) {
      toast.error(refusal(err, 'Could not remove them'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card as="section" aria-labelledby="household-members" flush className="overflow-hidden">
      <div className="px-4 pt-4 md:px-5 md:pt-5">
        <CardHeader id="household-members" title="Members" />
      </div>
      {list.isError ? (
        <ErrorState title="Couldn't load the members" message={errorMessage(list.error, 'Reload the page to try again.')} onRetry={() => list.refetch()} />
      ) : list.isLoading ? (
        <SkeletonList rows={2} label="Loading members" />
      ) : (
        <ul aria-label="Members" className="divide-y divide-border border-t border-border">
          {(list.data ?? []).map((m) => {
            const self = m.user_id === myId
            const second = m.display_name ? `${m.username} · ${m.email}` : m.username && m.username !== m.email ? m.email : ''
            return (
              <li key={m.user_id} className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3 md:px-5">
                <span className="min-w-0 flex-1">
                  <span className="block text-body text-text">
                    {nameOf(m)}
                    {self && <span className="text-text-muted"> (you)</span>}
                  </span>
                  {second && <span className="block truncate text-small text-text-muted">{second}</span>}
                </span>
                {isAdmin && !self ? (
                  <span className="flex items-center gap-1">
                    <Select
                      aria-label={`Role of ${nameOf(m)}`}
                      value={m.role}
                      options={ROLE_OPTIONS}
                      onValueChange={(r) => void changeRole(m, r as HouseholdRole)}
                      wrapperClassName="w-36"
                    />
                    <Button size="icon" variant="ghost" aria-label={`Remove ${nameOf(m)}`} onClick={() => setRemoving(m)}>
                      <UserMinus size={16} aria-hidden />
                    </Button>
                  </span>
                ) : (
                  <Badge tone={m.role === 'admin' ? 'primary' : 'neutral'}>{ROLE_LABEL[m.role] ?? m.role}</Badge>
                )}
              </li>
            )
          })}
        </ul>
      )}
      <Dialog
        open={removing !== null}
        onClose={() => !busy && setRemoving(null)}
        dismissible={!busy}
        title={removing ? `Remove ${nameOf(removing)}?` : 'Remove?'}
        description="They lose access to this household’s nodes, devices and settings. An invite brings them back."
        footer={
          <>
            <Button disabled={busy} onClick={() => setRemoving(null)}>
              Keep them
            </Button>
            <Button variant="danger" loading={busy} onClick={() => void remove()}>
              Remove
            </Button>
          </>
        }
      />
    </Card>
  )
}

const EXPIRY_OPTIONS = [1, 7, 30, 90].map((d) => ({
  value: String(d),
  label: d === 1 ? '1 day' : `${d} days`,
}))
const USES_OPTIONS = [
  { value: '1', label: 'Once' },
  { value: '5', label: '5 people' },
  { value: '', label: 'No limit' },
]

function InvitesCard({ id, isAdmin }: { id: string; isAdmin: boolean }) {
  const qc = useQueryClient()
  const key = ['household', id, 'invites']
  const list = useQuery({
    queryKey: key,
    queryFn: () => listInvites(id),
    retry: false,
  })
  const [role, setRole] = useState<HouseholdRole>('member')
  const [days, setDays] = useState('7')
  const [uses, setUses] = useState('1')
  const [creating, setCreating] = useState(false)
  const [made, setMade] = useState<Invite | null>(null)
  const [revoking, setRevoking] = useState<Invite | null>(null)
  const [busy, setBusy] = useState(false)
  // Only codes that still work: not revoked, not used up, not expired.
  const [now] = useState(() => Date.now()) // when the card opened; a code expiring meanwhile stays until reload
  const live = (list.data ?? []).filter(
    (i) => !i.revoked && (i.max_uses == null || i.use_count < i.max_uses) && !(i.expires_at && (parseServerTime(i.expires_at)?.getTime() ?? Infinity) <= now),
  )

  async function create() {
    setCreating(true)
    try {
      const inv = await createInvite(id, {
        default_role: role,
        expires_in_days: Number(days),
        max_uses: uses ? Number(uses) : null,
      })
      setMade(inv)
      await qc.invalidateQueries({ queryKey: key })
    } catch (err) {
      toast.error(refusal(err, 'Could not make an invite'))
    } finally {
      setCreating(false)
    }
  }

  async function revoke() {
    if (!revoking) return
    setBusy(true)
    try {
      await revokeInvite(id, revoking.id)
      await qc.invalidateQueries({ queryKey: key })
      if (made?.id === revoking.id) setMade(null)
      toast(`Revoked ${revoking.code}`)
      setRevoking(null)
    } catch (err) {
      toast.error(refusal(err, 'Could not revoke it'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card as="section" aria-labelledby="household-invites" className="space-y-4">
      <CardHeader
        id="household-invites"
        title="Invites"
        description="Someone enters the code when they sign up in the Jarvis Automation app, or under Join a household below."
      />
      <div className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-3">
          <Select
            label="Joins as"
            value={role}
            onValueChange={(v) => setRole(v as HouseholdRole)}
            options={ROLE_OPTIONS.filter((o) => isAdmin || o.value !== 'admin')}
          />
          <Select label="Expires after" value={days} onValueChange={setDays} options={EXPIRY_OPTIONS} />
          <Select label="Can be used" value={uses} onValueChange={setUses} options={USES_OPTIONS} />
        </div>
        <Button icon={<Plus size={16} aria-hidden />} loading={creating} onClick={() => void create()}>
          New invite code
        </Button>
        {made && <NewCode invite={made} />}
      </div>
      {list.isError ? (
        <p className="text-small text-danger">{errorMessage(list.error, 'Couldn’t load the invites.')}</p>
      ) : live.length > 0 ? (
        <ul aria-label="Invite codes" className="divide-y divide-border rounded-control border border-border">
          {live.map((i) => (
            <li key={i.id} className="flex flex-wrap items-center gap-x-3 px-3 py-2">
              <code className="font-mono text-body tracking-wider text-text">{i.code}</code>
              {/* Under the code on a phone, beside it from sm. */}
              <span className="order-last basis-full text-small text-text-muted sm:order-none sm:basis-auto sm:flex-1">
                {[
                  ROLE_LABEL[i.default_role] ?? i.default_role,
                  i.max_uses ? `${i.use_count} of ${i.max_uses} used` : `${i.use_count} used`,
                  i.expires_at ? `expires ${formatServerTime(i.expires_at, { dateStyle: 'medium' })}` : null,
                ]
                  .filter(Boolean)
                  .join(' · ')}
              </span>
              <Button size="sm" variant="ghost" className="ml-auto" aria-label={`Revoke ${i.code}`} onClick={() => setRevoking(i)}>
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        !list.isLoading && <p className="text-small text-text-muted">No open invites.</p>
      )}
      <Dialog
        open={revoking !== null}
        onClose={() => !busy && setRevoking(null)}
        dismissible={!busy}
        title={revoking ? `Revoke ${revoking.code}?` : 'Revoke?'}
        description="Nobody can join with it after this. People who already joined stay."
        footer={
          <>
            <Button disabled={busy} onClick={() => setRevoking(null)}>
              Keep it
            </Button>
            <Button variant="danger" loading={busy} onClick={() => void revoke()}>
              Revoke
            </Button>
          </>
        }
      />
    </Card>
  )
}

function NewCode({ invite }: { invite: Invite }) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      await navigator.clipboard.writeText(invite.code)
      setCopied(true)
    } catch {
      toast.error('Could not copy; select the code instead')
    }
  }
  return (
    <div role="status" className="flex flex-wrap items-center gap-3 rounded-control bg-primary-soft px-4 py-3">
      <span className="text-small text-text">New code:</span>
      <code className="font-mono text-subheading tracking-wider text-primary select-all">{invite.code}</code>
      <Button size="sm" icon={copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />} onClick={() => void copy()}>
        {copied ? 'Copied' : 'Copy'}
      </Button>
    </div>
  )
}

function JoinCard() {
  const { reloadHouseholds, switchHousehold } = useAuth()
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [joining, setJoining] = useState(false)
  async function join() {
    setJoining(true)
    setError(null)
    try {
      const joined = await joinHousehold(code.trim())
      await reloadHouseholds()
      await switchHousehold(joined.household_id)
      toast.success(`You joined ${joined.household_name}`)
      setCode('')
    } catch (err) {
      setError(refusal(err, 'That code didn’t work'))
    } finally {
      setJoining(false)
    }
  }
  return (
    <Card as="section" aria-labelledby="household-join">
      <CardHeader
        id="household-join"
        title="Join a household"
        description="Got an invite code from someone else’s household? Enter it to join; you keep this one too."
      />
      <form
        className="flex items-start gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (code.trim()) void join()
        }}
      >
        <Input
          label="Invite code"
          value={code}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setCode(e.target.value)}
          error={error ?? undefined}
          wrapperClassName="flex-1"
        />
        <Button type="submit" className="mt-6" loading={joining} disabled={!code.trim()}>
          Join
        </Button>
      </form>
    </Card>
  )
}

function LeaveCard({ id, name }: { id: string; name: string }) {
  const { households, reloadHouseholds, switchHousehold } = useAuth()
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  async function leave() {
    setBusy(true)
    try {
      await leaveHousehold(id)
      const next = households.find((h) => h.id !== id)
      if (next) await switchHousehold(next.id)
      await reloadHouseholds()
      toast(`You left ${name}`)
      setOpen(false)
    } catch (err) {
      toast.error(refusal(err, 'Could not leave'))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Card as="section" aria-labelledby="household-leave">
      <CardHeader
        id="household-leave"
        title="Leave this household"
        description="You lose access to its nodes, devices and settings. Your other households stay."
      />
      <Button variant="danger" icon={<LogOut size={16} aria-hidden />} onClick={() => setOpen(true)}>
        Leave {name}
      </Button>
      <Dialog
        open={open}
        onClose={() => !busy && setOpen(false)}
        dismissible={!busy}
        title={`Leave ${name}?`}
        description="To come back you’ll need a new invite code."
        footer={
          <>
            <Button disabled={busy} onClick={() => setOpen(false)}>
              Stay
            </Button>
            <Button variant="danger" loading={busy} onClick={() => void leave()}>
              Leave
            </Button>
          </>
        }
      />
    </Card>
  )
}

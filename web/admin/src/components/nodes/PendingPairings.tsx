import { useState } from 'react'
import { Check, Laptop, X } from 'lucide-react'
import { toast } from 'sonner'
import { useApprovePairing, useDenyPairing, usePairings } from '@/hooks/useNodes'
import { errorMessage } from '@/lib/errors'
import type { PairingHousehold, PendingPairing } from '@/api/nodes'

const inputClass =
  'rounded-md border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-2 py-1 text-sm text-[var(--color-text)]'

function PairingRow({ pairing, households }: { pairing: PendingPairing; households: PairingHousehold[] }) {
  const [household, setHousehold] = useState(households[0]?.id ?? '')
  const [room, setRoom] = useState('desktop')
  const approve = useApprovePairing()
  const deny = useDenyPairing()
  const busy = approve.isPending || deny.isPending

  const onApprove = () =>
    approve.mutate(
      { code: pairing.code, household_id: household, room: room.trim() || 'desktop' },
      {
        onSuccess: () => toast.success(`${pairing.name} approved. It finishes pairing in a few seconds.`),
        onError: (err) => toast.error(errorMessage(err, 'Could not approve the computer')),
      },
    )
  const onDeny = () =>
    deny.mutate(pairing.code, {
      onSuccess: () => toast.success(`${pairing.name} denied`),
      onError: (err) => toast.error(errorMessage(err, 'Could not deny the computer')),
    })

  return (
    <li className="space-y-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <Laptop size={16} className="shrink-0 text-[var(--color-text-muted)]" />
          <span className="truncate font-medium text-[var(--color-text)]">{pairing.name}</span>
          <span className="text-xs text-[var(--color-text-muted)]">
            {[pairing.platform, pairing.requested_from].filter(Boolean).join(' · ')}
          </span>
        </div>
        <code
          className="rounded-md bg-[var(--color-surface-alt)] px-2 py-1 font-mono text-lg tracking-widest text-[var(--color-text)]"
          aria-label="Pairing code"
        >
          {pairing.code}
        </code>
      </div>
      <p className="text-xs text-[var(--color-text-muted)]">
        Approve only if the computer shows this same code. It expires in {Math.max(1, Math.ceil(pairing.expires_in / 60))}{' '}
        min.
      </p>
      <div className="flex flex-wrap items-end gap-2">
        <label className="flex flex-col gap-1 text-xs text-[var(--color-text-muted)]">
          Household
          <select
            className={inputClass}
            value={household}
            onChange={(e) => setHousehold(e.target.value)}
            disabled={busy}
          >
            {households.map((h) => (
              <option key={h.id} value={h.id}>
                {h.name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs text-[var(--color-text-muted)]">
          Room
          <input className={inputClass} value={room} onChange={(e) => setRoom(e.target.value)} disabled={busy} />
        </label>
        <div className="ml-auto flex gap-2">
          <button
            onClick={onDeny}
            disabled={busy}
            className="inline-flex items-center gap-1 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:opacity-50"
          >
            <X size={14} /> Deny
          </button>
          <button
            onClick={onApprove}
            disabled={busy || !household}
            className="inline-flex items-center gap-1 rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-sm text-white hover:opacity-90 disabled:opacity-50"
          >
            <Check size={14} /> Approve
          </button>
        </div>
      </div>
    </li>
  )
}

/**
 * PendingPairings lists computers asking to join (device-code pairing, DESIGN §13). The
 * approver puts each into one of their own households; cc refuses any other. Nothing renders
 * while nothing waits, or when the caller belongs to no household.
 */
export default function PendingPairings() {
  const { data } = usePairings()
  const pairings = data?.pairings ?? []
  const households = data?.households ?? []
  if (pairings.length === 0 || households.length === 0) return null
  return (
    <section className="space-y-2">
      <h2 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">
        Pending pairings
      </h2>
      <ul className="space-y-2">
        {pairings.map((p) => (
          <PairingRow key={p.code} pairing={p} households={households} />
        ))}
      </ul>
    </section>
  )
}

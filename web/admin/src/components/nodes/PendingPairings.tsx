import { useState, type FormEvent } from 'react'
import { Check, X } from 'lucide-react'
import { toast } from 'sonner'
import { useApprovePairing, useDenyPairing, usePairings } from '@/hooks/useNodes'
import { useSystemInfo } from '@/hooks/useSystem'
import { errorMessage } from '@/lib/errors'
import { configURLFor } from './configUrl'
import type { PairingHousehold } from '@/api/nodes'

const inputClass =
  'rounded-md border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-2 py-1 text-sm text-[var(--color-text)]'

function ApproveForm({ households }: { households: PairingHousehold[] }) {
  const [code, setCode] = useState('')
  const [household, setHousehold] = useState(households[0]?.id ?? '')
  const [room, setRoom] = useState('desktop')
  const approve = useApprovePairing()
  const deny = useDenyPairing()
  const busy = approve.isPending || deny.isPending
  const typed = code.trim()
  const householdName = households.find((h) => h.id === household)?.name ?? household

  const onApprove = (e: FormEvent) => {
    e.preventDefault()
    approve.mutate(
      { code: typed, household_id: household, room: room.trim() || 'desktop' },
      {
        onSuccess: () => {
          setCode('')
          toast.success(`Approved. The computer asks to confirm ${householdName} before it joins.`)
        },
        onError: (err) => toast.error(errorMessage(err, 'Could not approve the computer')),
      },
    )
  }
  const onDeny = () =>
    deny.mutate(typed, {
      onSuccess: () => {
        setCode('')
        toast.success('Pairing denied')
      },
      onError: (err) => toast.error(errorMessage(err, 'Could not deny the computer')),
    })

  return (
    <form
      onSubmit={onApprove}
      className="space-y-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4"
    >
      <div className="flex flex-wrap items-end gap-2">
        <label className="flex flex-col gap-1 text-xs text-[var(--color-text-muted)]">
          Code shown on the computer
          <input
            className={`${inputClass} w-36 font-mono text-base uppercase tracking-widest`}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            placeholder="ABCD-EFGH"
            autoComplete="off"
            spellCheck={false}
            disabled={busy}
          />
        </label>
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
            type="button"
            onClick={onDeny}
            disabled={busy || !typed}
            className="inline-flex items-center gap-1 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:opacity-50"
          >
            <X size={14} /> Deny
          </button>
          <button
            type="submit"
            disabled={busy || !typed || !household}
            className="inline-flex items-center gap-1 rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-sm text-white hover:opacity-90 disabled:opacity-50"
          >
            <Check size={14} /> Approve
          </button>
        </div>
      </div>
      <p className="text-xs text-[var(--color-text-muted)]">
        Type the code from the computer's own screen. The computer then shows the household and asks before it joins.
      </p>
    </form>
  )
}

/** useConfigURL is this server's enroll address, or undefined when the page can't tell. */
function useConfigURL(systemInfo: boolean): string | undefined {
  const sys = useSystemInfo(systemInfo)
  const port = sys.data?.listeners?.find((l) => l.name === 'config')?.port ?? 7700
  return configURLFor(typeof window === 'undefined' ? '' : window.location.hostname, port)
}

/**
 * PendingPairings approves a computer asking to join (device-code pairing, DESIGN §13). The
 * approver types the code the computer shows: cc never lists the codes, so only someone who can
 * see that computer can approve it (loop #100). It goes into one of the approver's own
 * households; cc refuses any other. usePairings polls the count of computers waiting. Nothing
 * renders when the caller belongs to no household.
 */
export default function PendingPairings({ systemInfo = true }: { systemInfo?: boolean }) {
  const { data } = usePairings()
  // /api/system/info is the operator's: Add-ons passes false for a member and assumes the default port.
  const configURL = useConfigURL(systemInfo)
  const waiting = data?.waiting ?? 0
  const households = data?.households ?? []
  if (households.length === 0) return null
  return (
    <section className="space-y-2">
      <h2 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">
        Pending pairings
      </h2>
      {waiting === 0 ? (
        <p className="rounded-lg border border-dashed border-[var(--color-border)] p-4 text-sm text-[var(--color-text-muted)]">
          No computers waiting. On the computer, open Jarvis Automation Agent or run{' '}
          <code className="rounded bg-[var(--color-surface-alt)] px-1 font-mono text-[var(--color-text)]">
            jarvis-agent enroll --server {configURL ?? "<this server's address>"}
          </code>
          , then type the code it shows below.
        </p>
      ) : (
        <p className="text-sm text-[var(--color-text)]" data-testid="pairings-waiting">
          {waiting === 1 ? '1 computer is' : `${waiting} computers are`} waiting for approval. Type the code shown on
          the computer.
        </p>
      )}
      <ApproveForm households={households} />
    </section>
  )
}

import { useState, type FormEvent } from 'react'
import { Check, X } from 'lucide-react'
import { toast } from 'sonner'
import { Badge, Button, Card, CardHeader, Input, Select } from '@/components/ui'
import { useApprovePairing, useDenyPairing, usePairings } from '@/hooks/useNodes'
import { useSystemInfo } from '@/hooks/useSystem'
import { errorMessage } from '@/lib/errors'
import { configURLFor } from './configUrl'
import type { PairingHousehold } from '@/api/nodes'

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
    <form onSubmit={onApprove} className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-[minmax(9rem,11rem)_1fr_1fr] lg:grid-cols-[minmax(9rem,11rem)_1fr_1fr_auto] lg:items-end">
        <Input
          label="Code shown on the computer"
          className="font-mono tracking-widest uppercase"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          placeholder="ABCD-EFGH"
          autoComplete="off"
          spellCheck={false}
          disabled={busy}
        />
        <Select
          label="Household"
          value={household}
          onValueChange={setHousehold}
          disabled={busy}
          options={households.map((h) => ({ value: h.id, label: h.name }))}
        />
        <Input label="Room" value={room} onChange={(e) => setRoom(e.target.value)} disabled={busy} />
        <div className="flex gap-2 sm:col-span-3 sm:justify-end lg:col-span-1">
          <Button icon={<X size={16} aria-hidden />} onClick={onDeny} disabled={busy || !typed} loading={deny.isPending}>
            Deny
          </Button>
          <Button
            type="submit"
            variant="primary"
            icon={<Check size={16} aria-hidden />}
            disabled={busy || !typed || !household}
            loading={approve.isPending}
          >
            Approve
          </Button>
        </div>
      </div>
      <p className="text-small text-text-muted">
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
    <Card as="section" aria-labelledby="pending-pairings-title">
      <CardHeader
        id="pending-pairings-title"
        title="Pending pairings"
        actions={waiting > 0 && <Badge tone="warning" dot>{waiting} waiting</Badge>}
        description={
          waiting === 0 ? (
            <>
              No computers waiting. On the computer, open Jarvis Automation Agent or run{' '}
              <code className="rounded-control bg-surface-alt px-1 font-mono break-all text-text">
                jarvis-agent enroll --server {configURL ?? "<this server's address>"}
              </code>
              , then type the code it shows below.
            </>
          ) : (
            <span data-testid="pairings-waiting" className="text-text">
              {waiting === 1 ? '1 computer is' : `${waiting} computers are`} waiting for approval. Type the code shown on
              the computer.
            </span>
          )
        }
      />
      <ApproveForm households={households} />
    </Card>
  )
}

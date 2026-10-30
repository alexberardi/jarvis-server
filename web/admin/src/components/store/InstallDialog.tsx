import { useState } from 'react'
import { useQueries, useQuery } from '@tanstack/react-query'
import { AlertTriangle, Info } from 'lucide-react'
import type { NodeLiveness } from '@/api/nodes'
import { getStorePackage, pollInstall, requestInstall } from '@/api/store'
import type { StoreDetail } from '@/api/store'
import { Badge, Banner, Button, Dialog, Skeleton } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { useNodeLiveness } from '@/hooks/useNodes'
import { nodeLabel } from '@/lib/chatNode'
import { errorMessage } from '@/lib/errors'
import { aptPackages, nodeNames, nodeRoom, notForComputer } from '@/lib/store'

interface InstallDialogProps {
  /** The packages to install: one, or the default bundle. */
  names: string[]
  /** What is being installed, for the title ("Weather", "the default bundle"). */
  label: string
  /** The household whose Pantry the details come from. */
  householdId: string
  onClose: () => void
}

/** A started install: one package on one node, its request id or why it didn't start. */
interface Job {
  node: NodeLiveness
  pkg: StoreDetail
  requestId?: string
  error?: string
}

type Step = 'households' | 'nodes' | 'progress'

/**
 * InstallDialog installs packages on nodes the admin picks: first the households (skipped with
 * only one), then their nodes, then each node's progress. Each install is the ordinary per-node
 * request, so cc applies its household, role and Pantry checks to every one.
 */
export default function InstallDialog({ names, label, householdId, onClose }: InstallDialogProps) {
  const { households } = useAuth()
  const [step, setStep] = useState<Step>(households.length > 1 ? 'households' : 'nodes')
  const [chosenHH, setChosenHH] = useState<Set<string>>(() => new Set([householdId]))
  const [chosenNodes, setChosenNodes] = useState<Set<string>>(new Set())
  const [jobs, setJobs] = useState<Job[]>([])
  const [starting, setStarting] = useState(false)

  const details = useQueries({
    queries: names.map((n) => ({ queryKey: ['store-package', householdId, n], queryFn: () => getStorePackage(householdId, n) })),
  })
  const pkgs = details.flatMap((d) => (d.data ? [d.data] : []))
  const detailsError = details.find((d) => d.isError)?.error
  const nodes = useNodeLiveness()
  const apt = aptPackages(pkgs)
  const labels = nodeNames(nodes.data ?? [])

  /** runnable is what of the chosen packages a node can run (a computer can't run every one). */
  const runnable = (n: NodeLiveness) => (n.node_kind === 'desktop' ? pkgs.filter((p) => !notForComputer(p)) : pkgs)

  const start = async () => {
    setStarting(true)
    const picked = (nodes.data ?? []).filter((n) => chosenNodes.has(n.node_id) && chosenHH.has(n.household_id ?? ''))
    const planned = picked.flatMap((node) => runnable(node).map((pkg) => ({ node, pkg })))
    const started = await Promise.all(
      planned.map(async (j): Promise<Job> => {
        try {
          return { ...j, requestId: await requestInstall(j.node.node_id, j.pkg) }
        } catch (err) {
          return { ...j, error: errorMessage(err, 'Could not start the install') }
        }
      }),
    )
    setJobs(started)
    setStarting(false)
    setStep('progress')
  }

  const toggle = (set: Set<string>, id: string, on: boolean) => {
    const next = new Set(set)
    if (on) next.add(id)
    else next.delete(id)
    return next
  }

  const title = `Install ${label}`
  if (step === 'households') {
    return (
      <Dialog
        open
        onClose={onClose}
        title={title}
        description="Choose the households to install in. You pick their nodes next."
        footer={
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" disabled={chosenHH.size === 0} onClick={() => setStep('nodes')}>
              Next
            </Button>
          </>
        }
      >
        <fieldset className="space-y-1">
          <legend className="sr-only">Households</legend>
          {households.map((h) => (
            <label key={h.id} className="flex min-h-11 items-center gap-3 rounded-control px-2 text-body text-text hover:bg-surface-alt">
              <input
                type="checkbox"
                checked={chosenHH.has(h.id)}
                onChange={(e) => setChosenHH(toggle(chosenHH, h.id, e.target.checked))}
              />
              {h.name}
            </label>
          ))}
        </fieldset>
      </Dialog>
    )
  }

  if (step === 'progress') {
    return (
      <Dialog
        open
        onClose={onClose}
        title={title}
        description="Nodes keep installing if you close this."
        footer={
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        }
      >
        <ul className="divide-y divide-border" aria-label="Installs">
          {jobs.map((j) => (
            <JobRow
              key={`${j.node.node_id}/${j.pkg.command_name}`}
              job={j}
              name={labels.get(j.node.node_id) ?? nodeLabel(j.node)}
              showPackage={names.length > 1}
            />
          ))}
        </ul>
        <SetupNote />
      </Dialog>
    )
  }

  const byHousehold = households
    .filter((h) => chosenHH.has(h.id))
    .map((h) => ({ h, nodes: (nodes.data ?? []).filter((n) => n.household_id === h.id) }))
  const loading = nodes.isLoading || details.some((d) => d.isLoading)
  return (
    <Dialog
      open
      onClose={onClose}
      title={title}
      description="Choose the nodes to install on."
      footer={
        <>
          <Button variant="ghost" onClick={households.length > 1 ? () => setStep('households') : onClose}>
            {households.length > 1 ? 'Back' : 'Cancel'}
          </Button>
          <Button variant="primary" disabled={chosenNodes.size === 0 || pkgs.length === 0} loading={starting} onClick={start}>
            {chosenNodes.size === 0 ? 'Install' : chosenNodes.size === 1 ? 'Install on 1 node' : `Install on ${chosenNodes.size} nodes`}
          </Button>
        </>
      }
    >
      {detailsError || nodes.isError ? (
        <p role="alert" className="text-small text-danger">
          {errorMessage(detailsError ?? nodes.error, "Couldn't load the packages or nodes.")}
        </p>
      ) : loading ? (
        <div role="status" aria-label="Loading nodes" className="space-y-2">
          <Skeleton className="h-11 w-full" />
          <Skeleton className="h-11 w-full" />
        </div>
      ) : (
        <div className="space-y-4">
          {byHousehold.map(({ h, nodes: list }) => (
            <fieldset key={h.id} className="space-y-1">
              <legend className="mb-1 text-small font-medium text-text-muted">{h.name}</legend>
              {list.length === 0 && <p className="px-2 text-small text-text-muted">No nodes in this household.</p>}
              {list.map((n) => {
                const can = runnable(n)
                const why = !n.online
                  ? 'Offline'
                  : can.length === 0
                    ? pkgs.length === 1
                      ? notForComputer(pkgs[0])
                      : 'None of these run on a computer'
                    : can.length < pkgs.length
                      ? `${pkgs.length - can.length} of ${pkgs.length} are Pi-only; they are skipped here`
                      : null
                const disabled = !n.online || can.length === 0
                return (
                  <label
                    key={n.node_id}
                    className="flex min-h-11 items-center gap-3 rounded-control px-2 text-body text-text hover:bg-surface-alt has-[:disabled]:opacity-60 has-[:disabled]:hover:bg-transparent"
                  >
                    <input
                      type="checkbox"
                      disabled={disabled}
                      checked={!disabled && chosenNodes.has(n.node_id)}
                      onChange={(e) => setChosenNodes(toggle(chosenNodes, n.node_id, e.target.checked))}
                    />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate">{labels.get(n.node_id)}</span>
                      {(nodeRoom(n) || why) && (
                        <span className="block text-small text-text-muted">{[nodeRoom(n), why].filter(Boolean).join(' · ')}</span>
                      )}
                    </span>
                    <Badge>{n.node_kind === 'desktop' ? 'Computer' : 'Pi'}</Badge>
                  </label>
                )
              })}
            </fieldset>
          ))}
          {apt.length > 0 && (
            <Banner tone="warning" icon={<AlertTriangle size={18} />} title="Installs system packages as root on Pi nodes">
              {apt.join(', ')}
            </Banner>
          )}
          <SetupNote />
        </div>
      )}
    </Dialog>
  )
}

const DONE = new Set(['completed', 'failed', 'expired'])

/** JobRow is one install's progress, polled until the node reports. */
function JobRow({ job, name, showPackage }: { job: Job; name: string; showPackage: boolean }) {
  const poll = useQuery({
    queryKey: ['install', job.node.node_id, job.requestId],
    queryFn: () => pollInstall(job.node.node_id, job.requestId!),
    enabled: !!job.requestId,
    refetchInterval: (q) => (q.state.data && DONE.has(q.state.data.status) ? false : 2000),
  })
  const status = poll.data?.status
  const failed = !!job.error || status === 'failed' || status === 'expired' || poll.isError
  const text = job.error
    ? job.error
    : poll.isError
      ? errorMessage(poll.error, "Couldn't check the install")
      : status === 'completed'
        ? 'Installed'
        : failed
          ? (poll.data?.error_message ?? 'Failed')
          : status === 'restarting'
            ? 'Restarting'
            : 'Installing'
  return (
    <li className="flex items-center gap-3 py-2.5" data-testid="install-job">
      <span className="min-w-0 flex-1">
        <span className="block truncate text-body text-text">{name}</span>
        {showPackage && <span className="block text-small text-text-muted">{job.pkg.display_name || job.pkg.command_name}</span>}
        {failed && <span className="block text-small text-danger">{text}</span>}
      </span>
      <Badge tone={status === 'completed' ? 'success' : failed ? 'danger' : 'neutral'} dot={!failed && status !== 'completed'}>
        {status === 'completed' ? 'Installed' : failed ? 'Failed' : text}
      </Badge>
    </li>
  )
}

/** SetupNote: a package's secrets stay with the mobile app (decision 2026-10-14 K2). */
function SetupNote() {
  return (
    <p className="mt-3 flex gap-2 text-small text-text-muted">
      <Info size={16} aria-hidden className="mt-0.5 shrink-0" />
      <span>
        A package that needs accounts or keys is set up in the mobile app, under the node's settings. Use Sync to copy
        them from a node that already has them.
      </span>
    </p>
  )
}

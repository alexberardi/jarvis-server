import { useState, type ReactNode } from 'react'
import { AlertTriangle, ArrowUpCircle, CheckCircle2, CircleSlash, Copy, ExternalLink, HelpCircle, RefreshCw } from 'lucide-react'
import { toast } from 'sonner'
import { useQueryClient } from '@tanstack/react-query'
import { applyUpdate, updateStatus, type LastUpgrade, type UpdateInfo } from '@/api/update'
import { getSystemInfo, waitForRestart } from '@/api/system'
import { Button, Card, ErrorState, PageHeader, Skeleton, Switch } from '@/components/ui'
import { useFeatureAvailable } from '@/hooks/useFeature'
import { updateKey, useCheckNow, useLastUpgrade, useSetUpdatesEnabled, useUpdateCheck } from '@/hooks/useUpdateCheck'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

type Tone = 'neutral' | 'success' | 'warning'

const toneIcon: Record<Tone, string> = {
  neutral: 'bg-surface-alt text-text-muted',
  success: 'bg-success-soft text-success',
  warning: 'bg-warning-soft text-warning',
}

function VerdictLine({ icon, tone, title, children }: { icon: ReactNode; tone: Tone; title: ReactNode; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-start gap-3" data-testid="verdict">
      <span aria-hidden className={cn('flex size-10 shrink-0 items-center justify-center rounded-pill', toneIcon[tone])}>
        {icon}
      </span>
      <div className="min-w-0 pt-0.5">
        <p className="text-subheading text-text">{title}</p>
        <p className="mt-0.5 text-small break-words text-text-muted">{children}</p>
      </div>
    </div>
  )
}

function Verdict({ info }: { info: UpdateInfo }) {
  const status = updateStatus(info)
  const checkedAt = info.checked_at ? new Date(info.checked_at).toLocaleString() : null
  switch (status) {
    case 'disabled':
      return (
        <VerdictLine icon={<CircleSlash size={20} />} tone="neutral" title="Update checks are off">
          Jarvis Automation doesn't contact GitHub, so it can't tell whether a newer version exists.
        </VerdictLine>
      )
    case 'unchecked':
      return (
        <VerdictLine icon={<AlertTriangle size={20} />} tone="warning" title="Couldn't check for updates">
          {info.reason ?? 'No check has succeeded yet.'}
        </VerdictLine>
      )
    case 'available':
      return (
        <VerdictLine
          icon={<ArrowUpCircle size={20} />}
          tone="success"
          title={`Jarvis Automation ${info.latest_version} is available${info.prerelease ? ' (pre-release)' : ''}`}
        >
          You have {info.current_version}
          {info.published_at ? ` · released ${new Date(info.published_at).toLocaleDateString()}` : ''}
          {checkedAt ? ` · checked ${checkedAt}` : ''}
        </VerdictLine>
      )
    case 'up_to_date':
      return (
        <VerdictLine icon={<CheckCircle2 size={20} />} tone="success" title="Jarvis Automation is up to date">
          {info.current_version} is the latest {info.prerelease ? 'pre-release' : 'release'}
          {checkedAt ? ` · checked ${checkedAt}` : ''}
        </VerdictLine>
      )
    default:
      return (
        <VerdictLine icon={<HelpCircle size={20} />} tone="neutral" title="No verdict for this build">
          {info.reason ?? 'This version cannot be compared with the releases.'}
          {info.latest_version ? ` Latest release: ${info.latest_version}.` : ''}
        </VerdictLine>
      )
  }
}

/**
 * LastOutcome is the last finished upgrade. A rollback that restored a database snapshot says
 * from when writes were discarded (#109): the snapshot was taken at staging, so it covers the
 * old version's last writes and everything the new version wrote before the rollback.
 */
function LastOutcome({ last }: { last: LastUpgrade }) {
  const at = new Date(last.at).toLocaleString()
  if (last.outcome === 'succeeded') {
    return (
      <p className="text-small text-text-muted" data-testid="last-upgrade">
        Last update: {last.from_version} → {last.to_version} on {at}.
      </p>
    )
  }
  const what =
    last.outcome === 'rolled_back'
      ? `The update to ${last.to_version} was rolled back to ${last.from_version} on ${at}`
      : `The update to ${last.to_version} failed on ${at}`
  return (
    <div className="flex items-start gap-2 rounded-control bg-warning-soft p-3 text-small text-text" data-testid="last-upgrade">
      <AlertTriangle size={16} aria-hidden className="mt-0.5 shrink-0 text-warning" />
      <div className="min-w-0 space-y-1">
        <p className="break-words">
          {what}
          {last.reason ? `: ${last.reason}` : '.'}
        </p>
        {last.outcome === 'rolled_back' &&
          (last.db_restored ? (
            <p className="font-medium">
              The database was restored from the snapshot taken before the update.{' '}
              {last.data_since
                ? `Data written since ${new Date(last.data_since).toLocaleString()} was rolled back with the database.`
                : 'Data written since the update was staged was rolled back with it.'}
            </p>
          ) : (
            <p>The database was kept as it was.</p>
          ))}
      </div>
    </div>
  )
}

const linkClass = 'text-primary underline-offset-2 hover:underline'

function CopyLine({ text }: { text: string }) {
  return (
    <div className="flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto rounded-control border border-border bg-surface-alt px-3 py-2 font-mono text-small break-all whitespace-pre-wrap text-text">
        {text}
      </pre>
      <Button
        variant="ghost"
        size="icon"
        aria-label="Copy command"
        title="Copy"
        onClick={() => void navigator.clipboard?.writeText(text).then(() => toast.success('Copied'), () => {})}
      >
        <Copy size={16} aria-hidden />
      </Button>
    </div>
  )
}

/**
 * UpdatePage (AD5): the opt-in, the verdict, and how to update. Every claim comes from
 * `updateStatus` (I1): with checks off or a failed check it never says "up to date". The
 * in-place update button appears when jarvisd has the apply route and hides on a 404.
 */
export default function UpdatePage() {
  const qc = useQueryClient()
  const { data, isLoading, isError, error, refetch } = useUpdateCheck()
  const check = useCheckNow()
  const toggle = useSetUpdatesEnabled()
  const canApply = useFeatureAvailable('update-apply')
  const { data: last } = useLastUpgrade()
  const [applying, setApplying] = useState(false)
  const [manual, setManual] = useState<{ detail: string; command?: string } | null>(null)

  async function apply(info: UpdateInfo) {
    if (!window.confirm(`Update Jarvis Automation to ${info.latest_version}? It restarts, so voice and the admin pause briefly.`)) return
    setApplying(true)
    setManual(null)
    let before: string | undefined
    try {
      before = (await getSystemInfo()).started_at
    } catch {
      // ignore
    }
    try {
      const r = await applyUpdate(info.latest_version)
      if (r.kind === 'unsupported') {
        toast.info("This install can't update itself yet. Use the command below.")
        return
      }
      if (r.kind === 'manual') {
        setManual({ detail: r.detail, command: r.command })
        return
      }
      const id = toast.loading('Updating Jarvis Automation… It restarts when the new version is in place.')
      const after = await waitForRestart(before, 5 * 60_000, 2000)
      if (!after) {
        toast.error('Jarvis Automation didn\'t come back within 5 minutes. Check it on the server.', { id })
        return
      }
      toast.success(`Jarvis Automation ${after.version} is running`, { id })
      await qc.invalidateQueries({ queryKey: updateKey })
      void qc.invalidateQueries({ queryKey: ['system-info'] })
    } catch (err) {
      toast.error(errorMessage(err, 'The update failed'))
    } finally {
      setApplying(false)
    }
  }

  const checkNow = () => check.mutate(undefined, { onError: (err) => toast.error(errorMessage(err, 'Check failed')) })

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title="Updates" description="New Jarvis Automation releases come from GitHub. Nothing installs by itself." />

      {isLoading && (
        <Card role="status" aria-label="Loading the update status" className="space-y-3">
          <Skeleton className="h-6 w-1/2" />
          <Skeleton className="h-4 w-3/4" />
        </Card>
      )}
      {isError && (
        <Card>
          <ErrorState
            title="Couldn't read the update status"
            message={errorMessage(error)}
            onRetry={() => refetch()}
          />
        </Card>
      )}

      {data && (
        <div className="space-y-4">
          <Card>
            <Switch
              label="Check for updates"
              description="Jarvis Automation asks GitHub for the release list: at most once an hour, when the admin shows the update status, and when you press Check now. GitHub sees your IP address. Off by default."
              checked={data.updates_enabled}
              disabled={toggle.isPending}
              onCheckedChange={(on) => {
                toggle.mutate(on, {
                  onError: (err) => toast.error(errorMessage(err, 'Could not change the setting')),
                  // Opting in checks at once (A10b), instead of showing "Couldn't check" until Check now.
                  onSuccess: () => {
                    if (on) checkNow()
                  },
                })
              }}
            />
          </Card>

          <Card className="space-y-4">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <Verdict info={data} />
              {data.updates_enabled && (
                <Button
                  size="sm"
                  className="self-start"
                  icon={<RefreshCw size={14} aria-hidden />}
                  loading={check.isPending}
                  onClick={checkNow}
                >
                  Check now
                </Button>
              )}
            </div>
            <p className="text-small text-text-muted">
              Running {data.current_version} on {data.platform}.
            </p>
            {last && <LastOutcome last={last} />}

            {updateStatus(data) === 'available' && (
              <div className="space-y-4 border-t border-border pt-4">
                {canApply && (
                  <Button
                    variant="primary"
                    icon={<ArrowUpCircle size={16} aria-hidden />}
                    loading={applying}
                    onClick={() => void apply(data)}
                  >
                    Update now
                  </Button>
                )}
                {manual && (
                  <div role="status" className="space-y-2 rounded-control bg-warning-soft p-3 text-small text-text">
                    <p>{manual.detail}</p>
                    {manual.command && <CopyLine text={manual.command} />}
                  </div>
                )}
                {data.install_command ? (
                  <div className="space-y-1.5">
                    <p className="text-small text-text-muted">Or update from a terminal on the server:</p>
                    <CopyLine text={data.install_command} />
                  </div>
                ) : (
                  <p className="text-small text-text-muted">{data.install_hint}</p>
                )}
                {(data.asset || data.release_url) && (
                  <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-small text-text-muted">
                    {data.asset && (
                      <span>
                        Download:{' '}
                        <a className={linkClass} href={data.asset.url} target="_blank" rel="noreferrer">
                          {data.asset.name}
                        </a>
                        {data.checksums_url && (
                          <>
                            {' '}·{' '}
                            <a className={linkClass} href={data.checksums_url} target="_blank" rel="noreferrer">
                              SHA256SUMS
                            </a>
                          </>
                        )}
                      </span>
                    )}
                    {data.release_url && (
                      <a className={cn(linkClass, 'inline-flex items-center gap-1')} href={data.release_url} target="_blank" rel="noreferrer">
                        Release on GitHub <ExternalLink size={12} aria-hidden />
                      </a>
                    )}
                  </p>
                )}
                {data.release_notes && (
                  <details className="group">
                    <summary className="cursor-pointer rounded-control text-small font-medium text-text">Release notes</summary>
                    <pre className="mt-2 max-h-80 overflow-auto rounded-control bg-surface-alt p-3 font-mono text-caption whitespace-pre-wrap text-text">
                      {data.release_notes}
                    </pre>
                  </details>
                )}
              </div>
            )}
          </Card>
        </div>
      )}
    </div>
  )
}

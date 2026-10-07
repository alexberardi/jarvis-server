import { useState } from 'react'
import { AlertTriangle, ArrowUpCircle, CheckCircle2, CircleSlash, Copy, ExternalLink, HelpCircle, Loader2, RefreshCw } from 'lucide-react'
import { toast } from 'sonner'
import { useQueryClient } from '@tanstack/react-query'
import { applyUpdate, updateStatus, type UpdateInfo } from '@/api/update'
import { getSystemInfo, waitForRestart } from '@/api/system'
import { buttonClass } from '@/components/models/styles'
import { useFeatureAvailable } from '@/hooks/useFeature'
import { updateKey, useCheckNow, useSetUpdatesEnabled, useUpdateCheck } from '@/hooks/useUpdateCheck'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

function Verdict({ info }: { info: UpdateInfo }) {
  const status = updateStatus(info)
  const checkedAt = info.checked_at ? new Date(info.checked_at).toLocaleString() : null
  switch (status) {
    case 'disabled':
      return (
        <div className="flex items-start gap-3" data-testid="verdict">
          <CircleSlash size={20} className="mt-0.5 text-[var(--color-text-muted)]" />
          <div>
            <p className="text-sm font-medium text-[var(--color-text)]">Update checks are off</p>
            <p className="text-xs text-[var(--color-text-muted)]">
              jarvisd does not contact GitHub, so it can't tell whether a newer version exists.
            </p>
          </div>
        </div>
      )
    case 'unchecked':
      return (
        <div className="flex items-start gap-3" data-testid="verdict">
          <AlertTriangle size={20} className="mt-0.5 text-amber-500" />
          <div>
            <p className="text-sm font-medium text-[var(--color-text)]">Couldn't check for updates</p>
            <p className="text-xs text-[var(--color-text-muted)]">{info.reason ?? 'No check has succeeded yet.'}</p>
          </div>
        </div>
      )
    case 'available':
      return (
        <div className="flex items-start gap-3" data-testid="verdict">
          <ArrowUpCircle size={20} className="mt-0.5 text-green-500" />
          <div>
            <p className="text-sm font-medium text-[var(--color-text)]">
              jarvisd {info.latest_version} is available{info.prerelease ? ' (pre-release)' : ''}
            </p>
            <p className="text-xs text-[var(--color-text-muted)]">
              You have {info.current_version}
              {info.published_at ? ` · released ${new Date(info.published_at).toLocaleDateString()}` : ''}
              {checkedAt ? ` · checked ${checkedAt}` : ''}
            </p>
          </div>
        </div>
      )
    case 'up_to_date':
      return (
        <div className="flex items-start gap-3" data-testid="verdict">
          <CheckCircle2 size={20} className="mt-0.5 text-green-500" />
          <div>
            <p className="text-sm font-medium text-[var(--color-text)]">jarvisd is up to date</p>
            <p className="text-xs text-[var(--color-text-muted)]">
              {info.current_version} is the latest release{checkedAt ? ` · checked ${checkedAt}` : ''}
            </p>
          </div>
        </div>
      )
    default:
      return (
        <div className="flex items-start gap-3" data-testid="verdict">
          <HelpCircle size={20} className="mt-0.5 text-[var(--color-text-muted)]" />
          <div>
            <p className="text-sm font-medium text-[var(--color-text)]">No verdict for this build</p>
            <p className="text-xs text-[var(--color-text-muted)]">
              {info.reason ?? 'This version cannot be compared with the releases.'}
              {info.latest_version ? ` Latest release: ${info.latest_version}.` : ''}
            </p>
          </div>
        </div>
      )
  }
}

function CopyLine({ text }: { text: string }) {
  return (
    <div className="flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all rounded bg-[var(--color-surface-alt)] px-2 py-1.5 font-mono text-xs text-[var(--color-text)]">
        {text}
      </pre>
      <button
        type="button"
        className={buttonClass.icon}
        aria-label="Copy command"
        onClick={() => void navigator.clipboard?.writeText(text).then(() => toast.success('Copied'), () => {})}
      >
        <Copy size={14} />
      </button>
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
  const { data, isLoading, isError, error } = useUpdateCheck()
  const check = useCheckNow()
  const toggle = useSetUpdatesEnabled()
  const canApply = useFeatureAvailable('update-apply')
  const [applying, setApplying] = useState(false)
  const [manual, setManual] = useState<{ detail: string; command?: string } | null>(null)

  async function apply(info: UpdateInfo) {
    if (!window.confirm(`Update jarvisd to ${info.latest_version}? It restarts, so voice and the admin pause briefly.`)) return
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
        toast.info("This jarvisd can't update itself yet; use the command below.")
        return
      }
      if (r.kind === 'manual') {
        setManual({ detail: r.detail, command: r.command })
        return
      }
      const id = toast.loading('Updating jarvisd… it restarts when the new version is in place')
      const after = await waitForRestart(before, 5 * 60_000, 2000)
      if (!after) {
        toast.error('jarvisd did not come back within 5 minutes. Check it on the server.', { id })
        return
      }
      toast.success(`jarvisd ${after.version} is running`, { id })
      await qc.invalidateQueries({ queryKey: updateKey })
      void qc.invalidateQueries({ queryKey: ['system-info'] })
    } catch (err) {
      toast.error(errorMessage(err, 'The update failed'))
    } finally {
      setApplying(false)
    }
  }

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <div>
        <h1 className="text-xl font-bold text-[var(--color-text)]">Updates</h1>
        <p className="text-sm text-[var(--color-text-muted)]">New jarvisd releases come from GitHub. Nothing installs by itself.</p>
      </div>

      {isLoading && <p className="text-sm text-[var(--color-text-muted)]">Loading…</p>}
      {isError && <p className="text-sm text-red-500">{errorMessage(error, 'Could not read the update status')}</p>}

      {data && (
        <>
          <section className="space-y-3 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
            <label className="flex items-start gap-3">
              <input
                type="checkbox"
                role="switch"
                aria-label="Check for updates"
                className="mt-0.5 h-4 w-4 accent-[var(--color-primary)]"
                checked={data.updates_enabled}
                disabled={toggle.isPending}
                onChange={(e) =>
                  toggle.mutate(e.target.checked, { onError: (err) => toast.error(errorMessage(err, 'Could not change the setting')) })
                }
              />
              <span>
                <span className="block text-sm font-medium text-[var(--color-text)]">Check for updates</span>
                <span className="block text-xs text-[var(--color-text-muted)]">
                  jarvisd asks GitHub for the release list (at most once an hour, when the admin shows the update status, and
                  when you press Check now). GitHub sees your IP address. Off by default.
                </span>
              </span>
            </label>
          </section>

          <section className="space-y-3 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
            <div className="flex items-start justify-between gap-2">
              <Verdict info={data} />
              {data.updates_enabled && (
                <button
                  type="button"
                  className={buttonClass.secondary}
                  disabled={check.isPending}
                  onClick={() => check.mutate(undefined, { onError: (err) => toast.error(errorMessage(err, 'Check failed')) })}
                >
                  <RefreshCw size={12} className={cn(check.isPending && 'animate-spin')} /> Check now
                </button>
              )}
            </div>
            <p className="text-xs text-[var(--color-text-muted)]">
              Running {data.current_version} on {data.platform}.
            </p>

            {updateStatus(data) === 'available' && (
              <div className="space-y-3 border-t border-[var(--color-border)] pt-3">
                {canApply && (
                  <button type="button" className={buttonClass.primary} disabled={applying} onClick={() => void apply(data)}>
                    {applying ? <Loader2 size={12} className="animate-spin" /> : <ArrowUpCircle size={12} />} Update now
                  </button>
                )}
                {manual && (
                  <div className="space-y-1 rounded-lg border border-amber-500/30 bg-amber-500/5 p-2 text-xs text-[var(--color-text)]">
                    <p>{manual.detail}</p>
                    {manual.command && <CopyLine text={manual.command} />}
                  </div>
                )}
                {data.install_command ? (
                  <div className="space-y-1">
                    <p className="text-xs text-[var(--color-text-muted)]">Or update from a terminal on the server:</p>
                    <CopyLine text={data.install_command} />
                  </div>
                ) : (
                  <p className="text-xs text-[var(--color-text-muted)]">{data.install_hint}</p>
                )}
                {data.asset && (
                  <p className="text-xs text-[var(--color-text-muted)]">
                    Download:{' '}
                    <a className="text-[var(--color-primary)] hover:underline" href={data.asset.url} target="_blank" rel="noreferrer">
                      {data.asset.name}
                    </a>
                    {data.checksums_url && (
                      <>
                        {' '}·{' '}
                        <a className="text-[var(--color-primary)] hover:underline" href={data.checksums_url} target="_blank" rel="noreferrer">
                          SHA256SUMS
                        </a>
                      </>
                    )}
                  </p>
                )}
                {data.release_notes && (
                  <details>
                    <summary className="cursor-pointer text-xs text-[var(--color-text)]">Release notes</summary>
                    <pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap rounded bg-[var(--color-surface-alt)] p-2 text-xs text-[var(--color-text-muted)]">
                      {data.release_notes}
                    </pre>
                  </details>
                )}
                {data.release_url && (
                  <a
                    className="inline-flex items-center gap-1 text-xs text-[var(--color-primary)] hover:underline"
                    href={data.release_url}
                    target="_blank"
                    rel="noreferrer"
                  >
                    Release on GitHub <ExternalLink size={10} />
                  </a>
                )}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}

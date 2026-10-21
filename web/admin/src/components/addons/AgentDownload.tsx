import { useEffect, useState } from 'react'
import { CheckCircle2, Download, ExternalLink, Loader2, ShieldCheck } from 'lucide-react'
import { AgentDownloadError, fetchAgent, saveFile } from '@/api/addons'
import { AGENT_TARGETS, agentTarget, detectAgentTarget, type AgentTarget } from '@/lib/agentDownload'
import { LINKS } from '@/lib/addons'
import { buttonClass } from '@/components/models/styles'

type NavigatorUAData = { getHighEntropyValues?: (hints: string[]) => Promise<{ architecture?: string }> }

/**
 * useDetectedTarget guesses this computer's build from the user agent, then refines the
 * architecture from Chromium's client hints when they answer (an ARM Linux laptop).
 */
function useDetectedTarget(): AgentTarget | null {
  const [target, setTarget] = useState(() =>
    typeof navigator === 'undefined' ? null : detectAgentTarget(navigator.userAgent, navigator.platform),
  )
  useEffect(() => {
    const uad = (navigator as Navigator & { userAgentData?: NavigatorUAData }).userAgentData
    if (!uad?.getHighEntropyValues) return
    let live = true
    uad
      .getHighEntropyValues(['architecture'])
      .then((v) => {
        if (live && v.architecture) setTarget(detectAgentTarget(navigator.userAgent, navigator.platform, v.architecture))
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [])
  return target
}

type Status =
  | { kind: 'idle' }
  | { kind: 'busy'; done: number; total?: number }
  | { kind: 'saved'; filename: string; release: string | null }
  | { kind: 'failed'; message: string; releaseUrl: string }

function mb(n: number): string {
  return `${(n / (1 << 20)).toFixed(n < 10 << 20 ? 1 : 0)} MB`
}

/**
 * AgentDownload is the desktop agent's download button with a picker for other computers
 * (install plan §3.1 step 1). The file comes through jarvisd, which verified it against the
 * release signature first; the first download of a release takes a moment while it does.
 * onSaved tells the wizard the download step is done; size matches the page's buttons.
 */
export default function AgentDownload({ onSaved, size = 'xs' }: { onSaved?: () => void; size?: 'xs' | 'sm' }) {
  const detected = useDetectedTarget()
  const [picked, setPicked] = useState<string | null>(null)
  const target = (picked ? agentTarget(picked) : null) ?? detected
  const [status, setStatus] = useState<Status>({ kind: 'idle' })

  async function download() {
    if (!target) return
    setStatus({ kind: 'busy', done: 0 })
    try {
      const file = await fetchAgent(target, (done, total) => setStatus({ kind: 'busy', done, total }))
      saveFile(file)
      setStatus({ kind: 'saved', filename: file.filename, release: file.release })
      onSaved?.()
    } catch (err) {
      const e =
        err instanceof AgentDownloadError
          ? err
          : new AgentDownloadError('The download failed. Get it from the release page.', undefined, LINKS.releases)
      setStatus({ kind: 'failed', message: e.message, releaseUrl: e.releaseUrl })
    }
  }

  const busy = status.kind === 'busy'
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-end gap-2">
        <button
          type="button"
          className={size === 'sm' ? buttonClass.primary.replace('text-xs', 'text-sm') : buttonClass.primary}
          disabled={!target || busy}
          onClick={() => void download()}
        >
          {busy ? <Loader2 size={14} className="animate-spin" aria-hidden="true" /> : <Download size={14} aria-hidden="true" />}
          {target ? `Download for ${target.short}` : 'Download'}
        </button>
        <label className="flex min-w-0 flex-col gap-1 text-xs text-[var(--color-text-muted)]">
          {target ? 'Another computer?' : 'Choose your computer'}
          <select
            className="max-w-full rounded-md border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-2 py-1 text-sm text-[var(--color-text)]"
            value={target?.id ?? ''}
            disabled={busy}
            onChange={(e) => setPicked(e.target.value || null)}
          >
            {!target && <option value="">Choose…</option>}
            {AGENT_TARGETS.map((t) => (
              <option key={t.id} value={t.id}>
                {t.label}
              </option>
            ))}
          </select>
        </label>
      </div>
      <div aria-live="polite" className="text-xs">
        {busy && (
          <p className="text-[var(--color-text-muted)]" role="status">
            {status.done > 0
              ? `Downloading… ${mb(status.done)}${status.total ? ` of ${mb(status.total)}` : ''}`
              : 'Getting a verified copy from the release. The first download takes a moment.'}
          </p>
        )}
        {status.kind === 'saved' && (
          <div className="space-y-1">
            <p className="flex items-center gap-1.5 text-success" role="status">
              <CheckCircle2 size={14} aria-hidden="true" /> Saved {status.filename}
            </p>
            <p className="flex items-start gap-1.5 text-[var(--color-text-muted)]">
              <ShieldCheck size={14} className="mt-px shrink-0" aria-hidden="true" />
              <span>
                Checked against the signed release{status.release ? ` ${status.release}` : ''} before it was sent. {target?.hint}
              </span>
            </p>
          </div>
        )}
        {status.kind === 'failed' && (
          <p className="text-danger" role="alert">
            {status.message}{' '}
            <a
              href={status.releaseUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-0.5 underline"
            >
              Release page <ExternalLink size={12} aria-hidden="true" />
            </a>
          </p>
        )}
      </div>
    </div>
  )
}

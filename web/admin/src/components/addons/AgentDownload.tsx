import { useEffect, useState } from 'react'
import { CheckCircle2, Download, ExternalLink, ShieldCheck } from 'lucide-react'
import { AgentDownloadError, fetchAgent, saveFile } from '@/api/addons'
import { AGENT_TARGETS, agentTarget, detectAgentTarget, type AgentTarget } from '@/lib/agentDownload'
import { LINKS } from '@/lib/addons'
import { Button, Select } from '@/components/ui'

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
        <Button
          variant="primary"
          size={size === 'sm' ? 'md' : 'sm'}
          icon={<Download size={16} aria-hidden="true" />}
          loading={busy}
          disabled={!target}
          onClick={() => void download()}
        >
          {target ? `Download for ${target.short}` : 'Download'}
        </Button>
        <Select
          label={target ? 'Another computer?' : 'Choose your computer'}
          wrapperClassName="min-w-0 max-w-full"
          value={target?.id ?? ''}
          disabled={busy}
          placeholder={target ? undefined : 'Choose…'}
          onValueChange={(v) => setPicked(v || null)}
          options={AGENT_TARGETS.map((t) => ({ value: t.id, label: t.label }))}
        />
      </div>
      <div aria-live="polite" className="text-small">
        {busy && (
          <p className="text-text-muted" role="status">
            {status.done > 0
              ? `Downloading… ${mb(status.done)}${status.total ? ` of ${mb(status.total)}` : ''}`
              : 'Getting a verified copy from the release. The first download takes a moment.'}
          </p>
        )}
        {status.kind === 'saved' && (
          <div className="space-y-1">
            <p className="flex items-center gap-1.5 text-success" role="status">
              <CheckCircle2 size={16} aria-hidden="true" /> Saved {status.filename}
            </p>
            <p className="flex items-start gap-1.5 text-text-muted">
              <ShieldCheck size={16} className="mt-px shrink-0" aria-hidden="true" />
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
              className="inline-flex items-center gap-0.5 underline underline-offset-2"
            >
              Release page <ExternalLink size={12} aria-hidden="true" />
            </a>
          </p>
        )}
      </div>
    </div>
  )
}

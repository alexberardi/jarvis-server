import { useState } from 'react'
import { AlertTriangle, CheckCircle2, Copy, XCircle } from 'lucide-react'
import type { DoctorCheck } from '@/api/doctor'
import { cn } from '@/lib/utils'

const ICON = {
  ok: <CheckCircle2 size={14} className="shrink-0 text-green-500" aria-label="ok" />,
  warn: <AlertTriangle size={14} className="shrink-0 text-amber-500" aria-label="warning" />,
  fail: <XCircle size={14} className="shrink-0 text-red-500" aria-label="failed" />,
}

function statusIcon(status: string) {
  return ICON[status as keyof typeof ICON] ?? ICON.warn
}

function CopyFix({ fix }: { fix: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="mt-1 flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all rounded bg-[var(--color-surface-alt)] px-2 py-1 font-mono text-[11px] text-[var(--color-text)]">
        {fix}
      </pre>
      <button
        type="button"
        title="Copy"
        aria-label="Copy fix"
        className="rounded p-1 text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-[var(--color-text)]"
        onClick={() => {
          void navigator.clipboard?.writeText(fix).then(
            () => {
              setCopied(true)
              setTimeout(() => setCopied(false), 1500)
            },
            () => {},
          )
        }}
      >
        {copied ? <CheckCircle2 size={12} className="text-green-500" /> : <Copy size={12} />}
      </button>
    </div>
  )
}

/**
 * DoctorChecks lists `jarvisd doctor` results: each check with its detail and, when it is not
 * ok, the exact fix with a copy button (jarvisd runs unprivileged, so it can't apply a firewall
 * rule itself). `onlyProblems` hides the passing checks.
 */
export default function DoctorChecks({ checks, onlyProblems = false }: { checks: DoctorCheck[]; onlyProblems?: boolean }) {
  const shown = onlyProblems ? checks.filter((c) => c.status !== 'ok') : checks
  if (shown.length === 0) {
    return <p className="text-xs text-[var(--color-text-muted)]">{onlyProblems ? 'Every check passed.' : 'No checks ran.'}</p>
  }
  return (
    <ul className="space-y-2">
      {shown.map((c, i) => (
        <li
          key={`${c.name}-${i}`}
          className={cn(
            'rounded-lg border p-2',
            c.status === 'ok' ? 'border-[var(--color-border)]' : c.status === 'fail' ? 'border-red-500/30' : 'border-amber-500/30',
          )}
        >
          <div className="flex items-start gap-2">
            {statusIcon(c.status)}
            <div className="min-w-0 flex-1">
              <p className="text-sm text-[var(--color-text)]">{c.name}</p>
              {c.detail && <p className="text-xs text-[var(--color-text-muted)]">{c.detail}</p>}
              {c.fix && c.status !== 'ok' && <CopyFix fix={c.fix} />}
            </div>
          </div>
        </li>
      ))}
    </ul>
  )
}

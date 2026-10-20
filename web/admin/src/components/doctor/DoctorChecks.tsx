import { Fragment, useState } from 'react'
import { AlertTriangle, CheckCircle2, Copy, XCircle } from 'lucide-react'
import type { DoctorCheck } from '@/api/doctor'
import { Button } from '@/components/ui'

const ICON = {
  ok: <CheckCircle2 size={16} className="mt-0.5 shrink-0 text-success" aria-label="ok" />,
  warn: <AlertTriangle size={16} className="mt-0.5 shrink-0 text-warning" aria-label="warning" />,
  fail: <XCircle size={16} className="mt-0.5 shrink-0 text-danger" aria-label="failed" />,
}

function statusIcon(status: string) {
  return ICON[status as keyof typeof ICON] ?? ICON.warn
}

/**
 * The fix as a command to copy: it wraps only at spaces, and after the commas of a list such
 * as `port 17700,17702,…` (a <wbr>, so nothing extra is copied), never inside a port, an
 * address or a flag. A token still too long for the box scrolls inside it.
 */
function CopyFix({ fix }: { fix: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="mt-2 flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto rounded-control bg-surface-alt px-3 py-2 font-mono text-caption break-normal whitespace-pre-wrap text-text">
        {fix.split(',').map((part, i) => (
          <Fragment key={i}>
            {i > 0 && (
              <>
                ,<wbr />
              </>
            )}
            {part}
          </Fragment>
        ))}
      </pre>
      <Button
        variant="ghost"
        size="icon"
        title="Copy"
        aria-label="Copy fix"
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
        {copied ? <CheckCircle2 size={16} className="text-success" aria-hidden /> : <Copy size={16} aria-hidden />}
      </Button>
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
    return <p className="text-small text-text-muted">{onlyProblems ? 'Every check passed.' : 'No checks ran.'}</p>
  }
  // Rows divided by hairlines, not boxes: the list sits inside a card already.
  return (
    <ul className="divide-y divide-border">
      {shown.map((c, i) => (
        <li key={`${c.name}-${i}`} className="flex items-start gap-3 py-3 first:pt-0 last:pb-0">
          {statusIcon(c.status)}
          <div className="min-w-0 flex-1">
            <p className="text-subheading text-text">{c.name}</p>
            {c.detail && <p className="text-small break-words text-text-muted">{c.detail}</p>}
            {c.fix && c.status !== 'ok' && <CopyFix fix={c.fix} />}
          </div>
        </li>
      ))}
    </ul>
  )
}

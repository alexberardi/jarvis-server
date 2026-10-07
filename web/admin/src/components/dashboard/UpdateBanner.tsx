import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowUpCircle } from 'lucide-react'
import { updateStatus } from '@/api/update'
import { useUpdateCheck } from '@/hooks/useUpdateCheck'

const DISMISSED_KEY = 'jarvis-update-dismissed-version'

function dismissedVersion(): string | null {
  try {
    return localStorage.getItem(DISMISSED_KEY)
  } catch {
    return null
  }
}

/** UpdateBanner appears only when a real check found a newer release (I1). */
export default function UpdateBanner() {
  const navigate = useNavigate()
  const { data } = useUpdateCheck()
  const [dismissed, setDismissed] = useState(dismissedVersion)

  if (!data || updateStatus(data) !== 'available' || !data.latest_version) return null
  if (dismissed === data.latest_version) return null

  return (
    <div className="flex items-center justify-between rounded-lg border border-green-500/30 bg-green-500/5 p-4">
      <div className="flex items-center gap-3">
        <ArrowUpCircle size={20} className="text-green-500" />
        <div>
          <p className="text-sm font-medium text-[var(--color-text)]">jarvisd {data.latest_version} is available</p>
          <p className="text-xs text-[var(--color-text-muted)]">You have {data.current_version}</p>
        </div>
      </div>
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={() => {
            try {
              localStorage.setItem(DISMISSED_KEY, data.latest_version!)
            } catch {
              // ignore
            }
            setDismissed(data.latest_version)
          }}
          className="text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
        >
          Dismiss
        </button>
        <button
          type="button"
          onClick={() => navigate('/update')}
          className="rounded-lg bg-green-600 px-3 py-1.5 text-xs text-white hover:opacity-90"
        >
          View update
        </button>
      </div>
    </div>
  )
}

import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowUpCircle } from 'lucide-react'
import { useUpdateCheck } from '@/hooks/useUpdateCheck'

const DISMISSED_KEY = 'jarvis-update-dismissed-version'

export default function UpdateBanner() {
  const navigate = useNavigate()
  const { data } = useUpdateCheck()
  const [dismissed, setDismissed] = useState(() => {
    if (!data?.latestVersion) return false
    return localStorage.getItem(DISMISSED_KEY) === data.latestVersion
  })

  if (!data?.updateAvailable || dismissed) return null

  return (
    <div className="flex items-center justify-between rounded-lg border border-green-500/30 bg-green-500/5 p-4">
      <div className="flex items-center gap-3">
        <ArrowUpCircle size={20} className="text-green-500" />
        <div>
          <p className="text-sm font-medium text-[var(--color-text)]">
            Jarvis v{data.latestVersion} available
          </p>
          <p className="text-xs text-[var(--color-text-muted)]">
            You have v{data.currentVersion}
          </p>
        </div>
      </div>
      <div className="flex items-center gap-2">
        {data.releaseUrl && (
          <a
            href={data.releaseUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
          >
            Release notes
          </a>
        )}
        <button
          onClick={() => {
            localStorage.setItem(DISMISSED_KEY, data.latestVersion)
            setDismissed(true)
          }}
          className="text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
        >
          Dismiss
        </button>
        <button
          onClick={() => navigate('/update')}
          className="rounded-lg bg-green-600 px-3 py-1.5 text-xs text-white hover:opacity-90"
        >
          Update
        </button>
      </div>
    </div>
  )
}

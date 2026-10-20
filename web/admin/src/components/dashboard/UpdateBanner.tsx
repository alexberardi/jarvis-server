import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowUpCircle } from 'lucide-react'
import { updateStatus } from '@/api/update'
import { Banner, Button } from '@/components/ui'
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
    <Banner
      tone="success"
      icon={<ArrowUpCircle size={20} />}
      title={`jarvisd ${data.latest_version} is available`}
      actions={
        <>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              try {
                localStorage.setItem(DISMISSED_KEY, data.latest_version!)
              } catch {
                // ignore
              }
              setDismissed(data.latest_version)
            }}
          >
            Dismiss
          </Button>
          <Button variant="primary" size="sm" onClick={() => navigate('/admin/update')}>
            View update
          </Button>
        </>
      }
    >
      You have {data.current_version}
    </Banner>
  )
}

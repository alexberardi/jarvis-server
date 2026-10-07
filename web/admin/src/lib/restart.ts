import { toast } from 'sonner'
import { getSystemInfo, requestRestart, waitForRestart } from '@/api/system'
import { errorMessage } from './errors'

/**
 * restartJarvisd asks jarvisd to restart (AD8) and reports progress in one toast: under a
 * supervisor it waits for the new process; without one it shows the command to run; on a
 * jarvisd without the route it says so (and the button disappears, lib/features).
 */
export async function restartJarvisd(): Promise<void> {
  let before: string | undefined
  try {
    before = (await getSystemInfo()).started_at
  } catch {
    // Unknown start time: any answer after the request counts.
  }
  const id = toast.loading('Restarting jarvisd…')
  try {
    const r = await requestRestart()
    if (r.kind === 'unsupported') {
      toast.error("This jarvisd can't restart itself from the admin. Restart it on the server.", { id })
      return
    }
    if (r.kind === 'manual') {
      toast.warning(r.command ? `${r.detail}. Run on the server: ${r.command}` : r.detail, { id, duration: 30_000 })
      return
    }
    const info = await waitForRestart(before)
    if (info) toast.success('jarvisd restarted', { id })
    else toast.error('jarvisd did not come back within 2 minutes. Check it on the server.', { id })
  } catch (err) {
    toast.error(errorMessage(err, 'Restart failed'), { id })
  }
}

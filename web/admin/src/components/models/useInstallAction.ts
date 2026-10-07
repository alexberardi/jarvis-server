import { toast } from 'sonner'
import type { InstallRequest, InstallResponse } from '@/api/llm'
import { useStartInstall } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { isGatedError } from './logic'

/**
 * useInstallAction starts an install and reports it the way every caller should: the fit
 * warning as a warning (the install still runs, 06 §5), a gated repo as a request for the
 * Hugging Face token, anything else as an error.
 */
export function useInstallAction(onNeedsToken?: (reason: string) => void) {
  const start = useStartInstall()

  async function install(
    req: InstallRequest,
    name: string,
    onDone?: (res: InstallResponse) => void,
  ): Promise<InstallResponse | null> {
    try {
      const res = await start.mutateAsync(req)
      if (res.existing) toast.info(`${name} is already installing`)
      else toast.success(`Installing ${name}`)
      if (res.warning) toast.warning(res.warning, { duration: 15_000 })
      onDone?.(res)
      return res
    } catch (err) {
      if (isGatedError(err)) {
        onNeedsToken?.(errorMessage(err))
        toast.error(`${name} needs a Hugging Face token`)
      } else {
        toast.error(`Could not install ${name}: ${errorMessage(err)}`)
      }
      return null
    }
  }

  /** installAll starts several installs one after another (each one queues a durable job). */
  async function installAll(items: { req: InstallRequest; name: string }[]) {
    for (const it of items) await install(it.req, it.name)
  }

  return { install, installAll, isPending: start.isPending }
}

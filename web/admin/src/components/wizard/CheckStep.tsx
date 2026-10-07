import { RefreshCw, Stethoscope } from 'lucide-react'
import DoctorChecks from '@/components/doctor/DoctorChecks'
import { useDoctor, useRerunDoctor } from '@/hooks/useSetup'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'
import { buttonClass } from '@/components/models/styles'

/**
 * CheckStep runs `jarvisd doctor` (GET /api/doctor, open until the first superuser exists):
 * does each listener answer, and will the host firewall let phones and nodes on the LAN in?
 * Problems warn but never block; each comes with the command that fixes it.
 */
export default function CheckStep() {
  const { data, isLoading, isError, error } = useDoctor()
  const rerun = useRerunDoctor()

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h2 className="flex items-center gap-2 text-xl font-bold text-[var(--color-text)]">
            <Stethoscope size={20} /> Check this machine
          </h2>
          <p className="mt-1 text-sm text-[var(--color-text-muted)]">
            jarvisd checks that its ports answer and that devices on your network can reach them. Anything flagged
            here can be fixed later; it does not stop setup.
          </p>
        </div>
        <button
          type="button"
          className={buttonClass.secondary}
          disabled={rerun.isPending}
          onClick={() => rerun.mutate()}
        >
          <RefreshCw size={12} className={cn(rerun.isPending && 'animate-spin')} /> Run again
        </button>
      </div>

      {isLoading && <p className="text-sm text-[var(--color-text-muted)]">Running checks…</p>}
      {isError && (
        <p className="text-sm text-amber-500">
          {errorMessage(error, 'The checks could not run')}. You can continue; run <code>jarvisd doctor</code> on the
          server to see them.
        </p>
      )}
      {data && (
        <>
          <p className="text-sm text-[var(--color-text)]" data-testid="doctor-summary">
            {data.status === 'ok'
              ? 'Everything looks good.'
              : data.status === 'fail'
                ? 'Some checks failed. Jarvis will still work on this machine, but other devices may not reach it until they are fixed.'
                : 'A few things are worth a look.'}
          </p>
          <DoctorChecks checks={data.checks} />
        </>
      )}
    </div>
  )
}

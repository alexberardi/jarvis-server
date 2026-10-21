import { RefreshCw, Stethoscope } from 'lucide-react'
import DoctorChecks from '@/components/doctor/DoctorChecks'
import { Alert, Button } from '@/components/ui'
import { useDoctor, useRerunDoctor } from '@/hooks/useSetup'
import { errorMessage } from '@/lib/errors'
import { StepHeader, StepLoading } from './StepFrame'

/**
 * CheckStep runs `jarvisd doctor` (GET /api/doctor, open until the first superuser exists):
 * does each listener answer, and will the host firewall let phones and nodes on the LAN in?
 * Problems warn but never block; each comes with the command that fixes it.
 */
export default function CheckStep() {
  const { data, isLoading, isError, error } = useDoctor()
  const rerun = useRerunDoctor()

  return (
    <div className="space-y-5">
      <StepHeader
        icon={<Stethoscope size={22} />}
        title="Check this machine"
        actions={
          <Button size="sm" icon={<RefreshCw size={14} aria-hidden />} loading={rerun.isPending} onClick={() => rerun.mutate()}>
            Run again
          </Button>
        }
      >
        jarvisd checks that its ports answer and that devices on your network can reach them. Anything flagged here can
        be fixed later; it does not stop setup.
      </StepHeader>

      {isLoading && <StepLoading>Running the checks</StepLoading>}
      {isError && (
        <Alert tone="warning" title="The checks could not run">
          {errorMessage(error, 'The server did not answer')}. You can continue; run <code>jarvisd doctor</code> on the
          server to see them.
        </Alert>
      )}
      {data && (
        <>
          {data.status === 'ok' ? (
            <Alert tone="success">
              <span data-testid="doctor-summary">Everything looks good.</span>
            </Alert>
          ) : (
            <Alert tone="warning">
              <span data-testid="doctor-summary">
                {data.status === 'fail'
                  ? 'Some checks failed. Jarvis will still work on this machine, but other devices may not reach it until they are fixed.'
                  : 'A few things are worth a look.'}
              </span>
            </Alert>
          )}
          <DoctorChecks checks={data.checks} />
        </>
      )}
    </div>
  )
}

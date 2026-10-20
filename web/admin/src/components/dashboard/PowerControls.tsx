import { useState } from 'react'
import { Copy, Power, Square } from 'lucide-react'
import { toast } from 'sonner'
import { requestStop, type StopResult } from '@/api/system'
import { Button, Dialog } from '@/components/ui'
import { useFeatureAvailable } from '@/hooks/useFeature'
import { errorMessage } from '@/lib/errors'
import { restartJarvisd } from '@/lib/restart'
import type { SystemInfo } from '@/types/system'

function CopyCommand({ text }: { text: string }) {
  return (
    <div className="flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto rounded-control bg-surface-alt px-3 py-2 font-mono text-small break-all whitespace-pre-wrap text-text">
        {text}
      </pre>
      <Button
        variant="ghost"
        size="icon"
        aria-label="Copy command"
        title="Copy"
        onClick={() => void navigator.clipboard?.writeText(text).then(() => toast.success('Copied'), () => {})}
      >
        <Copy size={16} aria-hidden />
      </Button>
    </div>
  )
}

type Phase = { step: 'confirm' } | { step: 'sending' } | { step: 'done'; result: StopResult } | { step: 'failed'; error: string }

/**
 * StopDialog confirms the stop (AD8b) and then shows the final state: once jarvisd stops nothing
 * answers this page, so the start command is the last thing it can show.
 */
function StopDialog({ startCommand, startNote, onClose }: { startCommand: string; startNote: string; onClose: () => void }) {
  const [phase, setPhase] = useState<Phase>({ step: 'confirm' })
  const confirm = () => {
    setPhase({ step: 'sending' })
    requestStop().then(
      (result) => setPhase({ step: 'done', result }),
      (err) => setPhase({ step: 'failed', error: errorMessage(err, 'Stop failed') }),
    )
  }
  const result = phase.step === 'done' ? phase.result : null
  const stopping = result?.kind === 'stopping'
  const sending = phase.step === 'sending'
  return (
    <Dialog
      open
      onClose={onClose}
      // Once jarvisd is stopping this page can't come back: there is nothing to close into.
      dismissible={!sending && !stopping}
      title={stopping ? 'jarvisd is stopping' : 'Stop jarvisd?'}
      footer={
        phase.step === 'confirm' || sending ? (
          <>
            <Button onClick={onClose} disabled={sending}>
              Cancel
            </Button>
            <Button variant="danger" icon={<Square size={14} aria-hidden />} loading={sending} onClick={confirm}>
              Stop jarvisd
            </Button>
          </>
        ) : undefined
      }
    >
      <div className="space-y-3 text-body">
        {(phase.step === 'confirm' || sending) && (
          <>
            <p>Voice, the apps and this admin page stop working. jarvisd stays stopped until you start it again on the server:</p>
            <CopyCommand text={startCommand} />
            {startNote && <p className="text-small text-text-muted">{startNote}</p>}
          </>
        )}
        {result?.kind === 'stopping' && (
          <>
            <p>This page stops responding in a moment. To start jarvisd again, run:</p>
            <CopyCommand text={result.startCommand} />
            {result.startNote && <p className="text-small text-text-muted">{result.startNote}</p>}
          </>
        )}
        {result?.kind === 'refused' && (
          <>
            <p className="text-danger">{result.detail}</p>
            {result.command && <CopyCommand text={result.command} />}
          </>
        )}
        {result?.kind === 'unsupported' && <p className="text-danger">This jarvisd can't stop from the admin. Stop it on the server.</p>}
        {phase.step === 'failed' && <p className="text-danger">{phase.error}</p>}
      </div>
    </Dialog>
  )
}

/**
 * PowerControls are the Dashboard's Restart (AD8) and Stop (AD8b) buttons. Restart shows when a
 * supervisor can bring jarvisd back; Stop shows when this jarvisd has the route, and is disabled
 * with the reason when the installed service definition would restart it at once.
 */
export default function PowerControls({ info }: { info: SystemInfo }) {
  const restartRoute = useFeatureAvailable('restart')
  const [restarting, setRestarting] = useState(false)
  const [stopOpen, setStopOpen] = useState(false)
  const canRestart = restartRoute && (info.capabilities?.restart ?? info.restart_supported ?? false)
  const stop = info.stop
  const canStop = info.capabilities?.stop === true && stop !== undefined
  const stopTitle = canStop
    ? 'Stop jarvisd until it is started again on the server'
    : [stop?.reason, stop?.command && `Run on the server: ${stop.command}`].filter(Boolean).join(' ')
  return (
    <>
      {canRestart && (
        <Button
          size="sm"
          icon={<Power size={14} aria-hidden />}
          loading={restarting}
          onClick={() => {
            if (!window.confirm('Restart jarvisd now? Voice and the admin are unavailable for a few seconds.')) return
            setRestarting(true)
            void restartJarvisd().finally(() => setRestarting(false))
          }}
        >
          {restarting ? 'Restarting…' : 'Restart'}
        </Button>
      )}
      {stop && (
        // Stopping is rare and drastic: a quiet button here, the red one only in the dialog that confirms it.
        <Button variant="ghost" size="sm" icon={<Square size={14} aria-hidden />} disabled={!canStop} title={stopTitle} onClick={() => setStopOpen(true)}>
          Stop
        </Button>
      )}
      {stopOpen && stop && <StopDialog startCommand={stop.start_command} startNote={stop.start_note} onClose={() => setStopOpen(false)} />}
    </>
  )
}

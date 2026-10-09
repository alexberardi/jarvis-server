import { useState } from 'react'
import { Copy, Power, Square } from 'lucide-react'
import { toast } from 'sonner'
import { requestStop, type StopResult } from '@/api/system'
import { buttonClass } from '@/components/models/styles'
import { useFeatureAvailable } from '@/hooks/useFeature'
import { errorMessage } from '@/lib/errors'
import { restartJarvisd } from '@/lib/restart'
import type { SystemInfo } from '@/types/system'

function CopyCommand({ text }: { text: string }) {
  return (
    <div className="flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto whitespace-pre-wrap break-all rounded bg-[var(--color-surface-alt)] px-2 py-1.5 font-mono text-xs text-[var(--color-text)]">
        {text}
      </pre>
      <button
        type="button"
        className={buttonClass.icon}
        aria-label="Copy command"
        onClick={() => void navigator.clipboard?.writeText(text).then(() => toast.success('Copied'), () => {})}
      >
        <Copy size={14} />
      </button>
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
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true" aria-labelledby="stop-title">
      <div className="w-full max-w-lg space-y-3 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-xl">
        <h2 id="stop-title" className="flex items-center gap-2 text-lg font-semibold text-[var(--color-text)]">
          <Square size={16} /> {stopping ? 'jarvisd is stopping' : 'Stop jarvisd?'}
        </h2>
        {(phase.step === 'confirm' || phase.step === 'sending') && (
          <>
            <p className="text-sm text-[var(--color-text)]">
              Voice, the apps and this admin page stop working. jarvisd stays stopped until you start it again on the server:
            </p>
            <CopyCommand text={startCommand} />
            {startNote && <p className="text-xs text-[var(--color-text-muted)]">{startNote}</p>}
            <div className="flex justify-end gap-2 pt-1">
              <button type="button" className={buttonClass.secondary} onClick={onClose} disabled={phase.step === 'sending'}>
                Cancel
              </button>
              <button type="button" className={buttonClass.dangerSolid} onClick={confirm} disabled={phase.step === 'sending'}>
                <Square size={12} /> {phase.step === 'sending' ? 'Stopping…' : 'Stop jarvisd'}
              </button>
            </div>
          </>
        )}
        {result?.kind === 'stopping' && (
          <>
            <p className="text-sm text-[var(--color-text)]">This page stops responding in a moment. To start jarvisd again, run:</p>
            <CopyCommand text={result.startCommand} />
            {result.startNote && <p className="text-xs text-[var(--color-text-muted)]">{result.startNote}</p>}
          </>
        )}
        {result?.kind === 'refused' && (
          <>
            <p className="text-sm text-red-500">{result.detail}</p>
            {result.command && <CopyCommand text={result.command} />}
          </>
        )}
        {result?.kind === 'unsupported' && <p className="text-sm text-red-500">This jarvisd can't stop from the admin. Stop it on the server.</p>}
        {phase.step === 'failed' && <p className="text-sm text-red-500">{phase.error}</p>}
        {(phase.step === 'failed' || (result && !stopping)) && (
          <div className="flex justify-end">
            <button type="button" className={buttonClass.secondary} onClick={onClose}>
              Close
            </button>
          </div>
        )}
      </div>
    </div>
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
        <button
          type="button"
          className={buttonClass.secondary}
          disabled={restarting}
          onClick={() => {
            if (!window.confirm('Restart jarvisd now? Voice and the admin are unavailable for a few seconds.')) return
            setRestarting(true)
            void restartJarvisd().finally(() => setRestarting(false))
          }}
        >
          <Power size={12} /> {restarting ? 'Restarting…' : 'Restart'}
        </button>
      )}
      {stop && (
        <button type="button" className={buttonClass.danger} disabled={!canStop} title={stopTitle} onClick={() => setStopOpen(true)}>
          <Square size={12} /> Stop
        </button>
      )}
      {stopOpen && stop && <StopDialog startCommand={stop.start_command} startNote={stop.start_note} onClose={() => setStopOpen(false)} />}
    </>
  )
}

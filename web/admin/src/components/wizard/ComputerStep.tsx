import { useState } from 'react'
import type { ReactNode } from 'react'
import { CheckCircle2, Circle, ExternalLink, Laptop } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { getThisDevice } from '@/api/chat'
import { extensionState, getNodeLiveness } from '@/api/nodes'
import AgentDownload from '@/components/addons/AgentDownload'
import { Button, buttonClass, Spinner } from '@/components/ui'
import PendingPairings from '@/components/nodes/PendingPairings'
import { useAuth } from '@/hooks/useAuth'
import { LINKS, yourDevices } from '@/lib/addons'
import { detectAgentTarget, detectBrowser } from '@/lib/agentDownload'
import { nodeLabel } from '@/lib/chatNode'
import { cn } from '@/lib/utils'
import { StepActions, StepHeader } from './StepFrame'

/** How often the live checks refresh while the step is open. */
export const COMPUTER_POLL_MS = 3_000

function Tick({ done, children }: { done: boolean; children: ReactNode }) {
  return (
    <li className="flex items-start gap-2">
      {done ? (
        <CheckCircle2 size={18} className="mt-0.5 shrink-0 text-success" aria-label="Done" />
      ) : (
        <Circle size={18} className="mt-0.5 shrink-0 text-text-muted" aria-label="Not yet" />
      )}
      <div className="min-w-0 flex-1 text-body text-text">{children}</div>
    </li>
  )
}

function Option({
  checked,
  disabled,
  onChange,
  title,
  note,
}: {
  checked: boolean
  disabled?: boolean
  onChange: (v: boolean) => void
  title: string
  note: string
}) {
  return (
    <label
      className={cn(
        'flex items-start gap-3 rounded-control border p-3 transition-colors duration-(--duration-hover)',
        checked ? 'border-primary bg-primary-soft' : 'border-border',
        disabled ? 'cursor-not-allowed opacity-60' : 'cursor-pointer hover:border-border-strong',
      )}
    >
      <input
        type="checkbox"
        className="mt-1 size-4 shrink-0 accent-primary"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="min-w-0">
        <span className="block text-subheading text-text">{title}</span>
        <span className="block text-small text-text-muted">{note}</span>
      </span>
    </label>
  )
}

/**
 * ComputerStep is the wizard's optional "also use it on this computer" step (install plan §3.1,
 * item 3.3): the desktop agent, verified-downloaded through jarvisd, then paired by approving
 * the code it shows; and the browser extension, a store link only (no detection, decided Q2).
 * The ticks turn green by polling: this computer's agent from GET /api/addons/this-device (or,
 * when the server can't tell, an online desktop agent in the operator's households), its
 * extension from the agent's heartbeat. Skipping is always fine; Add-ons has all of it later.
 */
export default function ComputerStep({ onNext, onBack }: { onNext: () => void; onBack: () => void }) {
  const { households } = useAuth()
  const [agent, setAgent] = useState(false)
  const [browser, setBrowser] = useState(false)
  const [downloaded, setDownloaded] = useState(false)

  const hereQ = useQuery({
    queryKey: ['this-device'],
    queryFn: () => getThisDevice(),
    refetchInterval: agent ? COMPUTER_POLL_MS : false,
    enabled: agent,
    retry: false,
  })
  const nodesQ = useQuery({
    queryKey: ['addons-nodes'],
    queryFn: getNodeLiveness,
    refetchInterval: agent ? COMPUTER_POLL_MS : false,
    enabled: agent,
    retry: false,
  })
  const devices = yourDevices(
    nodesQ.data ?? [],
    households.map((h) => h.id),
  )
  const hereId = hereQ.data?.node_id ?? null
  const connected = (hereId ? devices.find((n) => n.node_id === hereId && n.online) : undefined) ?? devices.find((n) => n.online)
  const extConnected = connected ? extensionState(connected) === 'connected' : false
  const isMac = typeof navigator !== 'undefined' && detectAgentTarget(navigator.userAgent, navigator.platform)?.os === 'darwin'
  const kind = typeof navigator !== 'undefined' ? detectBrowser(navigator.userAgent) : 'other'
  const store = kind === 'firefox' ? LINKS.firefoxStore : kind === 'chrome' ? LINKS.chromeStore : null
  const storeName = kind === 'firefox' ? 'Firefox Add-ons' : 'the Chrome Web Store'
  const anything = agent || browser

  return (
    <div className="space-y-5">
      <StepHeader icon={<Laptop size={22} />} title="Also use Jarvis Automation on this computer">
        Optional. Talk to Jarvis from this computer and let it open apps, type and read the screen for you. You can do
        this later in <strong className="font-medium text-text">Add-ons</strong>.
      </StepHeader>

      <div className="space-y-3">
        <Option
          checked={agent}
          onChange={(v) => {
            setAgent(v)
            if (!v) setBrowser(false)
          }}
          title="Use Jarvis Automation on this computer (desktop agent)"
          note="An app that runs while you're signed in to this computer."
        />
        {agent && (
          <ol className="space-y-4 pl-3 sm:pl-7" aria-label="Desktop agent steps">
            <Tick done={downloaded || !!connected}>
              <p className="mb-2 font-medium">Download the agent</p>
              <AgentDownload onSaved={() => setDownloaded(true)} />
            </Tick>
            <Tick done={!!connected}>
              <p className="font-medium">Install it and approve its code</p>
              <p className="mb-2 text-small text-text-muted">
                Open the file you downloaded. The agent finds this server by itself and shows a code. Approve it here
                when the same code is on both screens.
              </p>
              {!connected && <PendingPairings />}
            </Tick>
            <Tick done={!!connected}>
              {connected ? (
                <p data-testid="agent-connected">
                  The agent is connected (<strong>{nodeLabel(connected)}</strong>).
                </p>
              ) : (
                <p className="flex items-center gap-2 text-text-muted" data-testid="agent-connected">
                  <Spinner /> Waiting for the agent to connect
                </p>
              )}
              {isMac && (
                <p className="mt-1 text-small text-text-muted">
                  On a Mac, the agent's own page asks for the permissions it needs (Microphone, Accessibility, Screen
                  Recording). Jarvis Automation can't grant them from here.
                </p>
              )}
            </Tick>
          </ol>
        )}

        <Option
          checked={browser}
          disabled={!agent}
          onChange={setBrowser}
          title="Let Jarvis control this browser (extension)"
          note={agent ? 'Reads and controls tabs through the desktop agent.' : 'Needs the desktop agent on this computer.'}
        />
        {browser && (
          <ol className="space-y-4 pl-3 sm:pl-7" aria-label="Browser extension steps">
            <Tick done={extConnected}>
              {store ? (
                <a href={store} target="_blank" rel="noopener noreferrer" className={buttonClass({ size: 'sm' })}>
                  Add it from {storeName} <ExternalLink size={14} aria-hidden="true" />
                </a>
              ) : (
                <p className="text-text-muted">
                  The store listings are on their way. Until then the desktop agent sets it up.{' '}
                  <a
                    href={LINKS.browserGuide}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-primary underline underline-offset-2"
                  >
                    How browser control works
                  </a>
                </p>
              )}
              <p className="mt-1 text-small text-text-muted" data-testid="extension-state">
                {extConnected
                  ? 'The extension is connected.'
                  : connected
                    ? 'Waiting for the extension to connect to the agent.'
                    : 'This turns green once the agent is connected and the extension is added.'}
              </p>
            </Tick>
          </ol>
        )}
      </div>

      <StepActions back={<Button onClick={onBack}>Back</Button>}>
        <Button variant="primary" onClick={onNext}>
          {anything ? 'Continue' : 'Skip for now'}
        </Button>
      </StepActions>
    </div>
  )
}

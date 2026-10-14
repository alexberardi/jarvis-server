import type { ReactNode } from 'react'
import { Download, ExternalLink, Laptop, Play, Puzzle, Smartphone } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { extensionState, getNodeLiveness } from '@/api/nodes'
import type { NodeLiveness } from '@/api/nodes'
import PendingPairings from '@/components/nodes/PendingPairings'
import { useAuth } from '@/hooks/useAuth'
import { AGENT_START_URL, detectOS, extensionLabel, LINKS, OS_NAME, yourDevices } from '@/lib/addons'
import { nodeLabel } from '@/lib/chatNode'
import { errorMessage } from '@/lib/errors'
import { timeAgo } from '@/lib/time'
import { cn } from '@/lib/utils'

/** How often device status refreshes, so a started agent turns green without a reload. */
export const DEVICE_POLL_MS = 10_000

const card = 'rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-5'
const linkButton =
  'inline-flex items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-alt)]'
const primaryButton =
  'inline-flex items-center gap-1.5 rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-sm font-medium text-white hover:opacity-90'

function Section({ title, icon, children }: { title: string; icon: ReactNode; children: ReactNode }) {
  return (
    <section className={card}>
      <h2 className="mb-3 flex items-center gap-2 font-semibold text-[var(--color-text)]">
        {icon}
        {title}
      </h2>
      {children}
    </section>
  )
}

function External({ href, children, primary }: { href: string; children: ReactNode; primary?: boolean }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" className={primary ? primaryButton : linkButton}>
      {children}
      <ExternalLink size={14} aria-hidden="true" />
    </a>
  )
}

function DeviceRow({ n }: { n: NodeLiveness }) {
  const ext = extensionState(n)
  return (
    <li className="flex flex-wrap items-center justify-between gap-2 py-3">
      <div className="min-w-0">
        <p className="flex items-center gap-2 font-medium text-[var(--color-text)]">
          <span
            aria-hidden="true"
            className={cn('h-2 w-2 shrink-0 rounded-full', n.online ? 'bg-green-500' : 'bg-[var(--color-text-muted)]')}
          />
          <span className="truncate">{nodeLabel(n)}</span>
        </p>
        <p className="text-xs text-[var(--color-text-muted)]">
          {n.online ? 'Online' : `Offline${n.last_seen ? ` · last seen ${timeAgo(n.last_seen)}` : ''}`}
          {' · '}
          <span data-testid="extension-state">{extensionLabel(ext)}</span>
        </p>
      </div>
      {/* TODO(webapp 01 P4): "Sign out" on a device needs cc's DELETE /admin/nodes/{id} on the
          gateway (a new member route, so a tier change for review) and a decision on what the
          agent does with the factory-reset it is sent. */}
      {!n.online && (
        <a href={AGENT_START_URL} className={linkButton} title="Works on the computer where the agent is installed">
          <Play size={14} aria-hidden="true" />
          Start
        </a>
      )}
    </li>
  )
}

/**
 * AddonsPage is where a member adds Jarvis to their computers, browsers and phone (install plan
 * §3.2). Minimal for v1: device status from cc's node list (scoped to the caller's households),
 * download links, store links (no extension detection, decided) and pairing approval.
 */
export default function AddonsPage() {
  const { state, households } = useAuth()
  const os = detectOS(navigator.userAgent, navigator.platform)
  const nodesQ = useQuery({
    queryKey: ['addons-nodes'],
    queryFn: getNodeLiveness,
    refetchInterval: DEVICE_POLL_MS,
    retry: false,
  })
  const devices = yourDevices(
    nodesQ.data ?? [],
    households.map((h) => h.id),
  )

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <div>
        <h1 className="text-xl font-semibold text-[var(--color-text)]">Add-ons</h1>
        <p className="text-sm text-[var(--color-text-muted)]">
          Use Jarvis on your computers, in your browser and on your phone.
        </p>
      </div>

      <Section title="Desktop agent" icon={<Laptop size={18} aria-hidden="true" />}>
        <p className="mb-3 text-sm text-[var(--color-text-muted)]">
          Talk to Jarvis with "Hey Jarvis" on a computer, and let it open apps, read the screen and type for you.
        </p>
        <div className="flex flex-wrap gap-2">
          {/* No agent installers in the release yet (install plan 2.1): the release page and the guide. */}
          <External href={LINKS.releases} primary>
            <Download size={14} aria-hidden="true" />
            Download for {OS_NAME[os]}
          </External>
          <External href={LINKS.agentGuide}>Install guide</External>
        </div>
      </Section>

      <Section title="Your devices" icon={<Laptop size={18} aria-hidden="true" />}>
        {nodesQ.isError ? (
          <p role="alert" className="text-sm text-red-500">
            {errorMessage(nodesQ.error, 'Could not load your devices')}
          </p>
        ) : nodesQ.isLoading ? (
          <p className="text-sm text-[var(--color-text-muted)]">Loading...</p>
        ) : devices.length === 0 ? (
          <p className="text-sm text-[var(--color-text-muted)]">
            No computers yet. Install the desktop agent, then approve its code below.
          </p>
        ) : (
          <ul className="divide-y divide-[var(--color-border)]" aria-label="Your devices">
            {devices.map((n) => (
              <DeviceRow key={n.node_id} n={n} />
            ))}
          </ul>
        )}
        <div className="mt-4">
          <PendingPairings systemInfo={Boolean(state.user?.is_superuser)} />
        </div>
      </Section>

      <Section title="Browser extension" icon={<Puzzle size={18} aria-hidden="true" />}>
        <p className="mb-3 text-sm text-[var(--color-text-muted)]">
          Lets Jarvis read and control tabs in your browser, through the desktop agent on the same computer.
        </p>
        <div className="flex flex-wrap gap-2">
          {LINKS.firefoxStore && <External href={LINKS.firefoxStore}>Add to Firefox</External>}
          {LINKS.chromeStore && (
<External href={LINKS.chromeStore}>Add to Chrome</External>
          )}
          {!LINKS.firefoxStore && !LINKS.chromeStore && (
            <>
              <p className="w-full text-sm text-[var(--color-text-muted)]">
                The store listings are on their way. Until then the desktop agent sets it up.
              </p>
              <External href={LINKS.browserGuide}>How browser control works</External>
            </>
          )}
        </div>
      </Section>

      <Section title="Phone app" icon={<Smartphone size={18} aria-hidden="true" />}>
        <p className="mb-3 text-sm text-[var(--color-text-muted)]">
          Chat, voice, your inbox and household settings on iPhone and Android.
        </p>
        <div className="flex flex-wrap gap-2">
          {LINKS.appStore && <External href={LINKS.appStore}>App Store</External>}
          {LINKS.playStore && <External href={LINKS.playStore}>Google Play</External>}
          {!LINKS.appStore && !LINKS.playStore && (
            <p className="text-sm text-[var(--color-text-muted)]">
              The Jarvis Automation app is in testing. Ask whoever set up Jarvis Automation for an invite.
            </p>
          )}
        </div>
      </Section>
    </div>
  )
}

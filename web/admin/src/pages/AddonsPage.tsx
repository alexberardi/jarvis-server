import { useState } from 'react'
import type { ReactNode } from 'react'
import { ExternalLink, Laptop, LogOut, Monitor, Play, Puzzle, Smartphone } from 'lucide-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getThisDevice } from '@/api/chat'
import { extensionState, getNodeLiveness, signOutDevice, websiteAccessState } from '@/api/nodes'
import type { NodeLiveness } from '@/api/nodes'
import AgentDownload from '@/components/addons/AgentDownload'
import PendingPairings from '@/components/nodes/PendingPairings'
import { useAuth } from '@/hooks/useAuth'
import {
  AGENT_START_URL,
  canSignOut,
  detectOS,
  extensionLabel,
  LINKS,
  OS_NAME,
  WEBSITE_ACCESS,
  websiteAccessFamilies,
  yourDevices,
} from '@/lib/addons'
import type { BrowserFamily } from '@/lib/addons'
import { nodeLabel } from '@/lib/chatNode'
import { errorMessage } from '@/lib/errors'
import { timeAgo } from '@/lib/time'
import { cn } from '@/lib/utils'

/** How often device status refreshes, so a started agent turns green without a reload. */
export const DEVICE_POLL_MS = 10_000

const card = 'rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-5'
const linkButton =
  'inline-flex items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-alt)]'
const dangerButton =
  'inline-flex items-center gap-1.5 rounded-lg bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:opacity-90 disabled:opacity-60'

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

function External({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" className={linkButton}>
      {children}
      <ExternalLink size={14} aria-hidden="true" />
    </a>
  )
}

/** WebsiteAccessSteps says how to turn on the extension's website access in the given browsers. */
function WebsiteAccessSteps({ families }: { families: BrowserFamily[] }) {
  return (
    <>
      <p>{WEBSITE_ACCESS.why}</p>
      <ul className="my-1 list-disc space-y-0.5 pl-5">
        {families.map((f) => (
          <li key={f}>{WEBSITE_ACCESS[f]}</li>
        ))}
      </ul>
      <p>{WEBSITE_ACCESS.reload}</p>
      <div className="mt-2">
        <External href={WEBSITE_ACCESS.docs}>More about website access</External>
      </div>
    </>
  )
}

/** SETTINGS_NOTE: node settings are end-to-end encrypted with a key only the phone app and the node hold. */
export const SETTINGS_NOTE = 'Settings for this computer are managed in the Jarvis Automation mobile app (admins).'

function DeviceRow({ n, here, maySignOut }: { n: NodeLiveness; here: boolean; maySignOut: boolean }) {
  const ext = extensionState(n)
  const accessOff = ext === 'connected' && websiteAccessState(n) === 'off'
  const qc = useQueryClient()
  const [confirming, setConfirming] = useState(false)
  const signOut = useMutation({
    mutationFn: () => signOutDevice(n.node_id),
    onSuccess: () => {
      setConfirming(false)
      void qc.invalidateQueries({ queryKey: ['addons-nodes'] })
      void qc.invalidateQueries({ queryKey: ['this-device'] })
    },
  })
  const label = nodeLabel(n)
  return (
    <li className="py-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="flex items-center gap-2 font-medium text-[var(--color-text)]">
            <span
              aria-hidden="true"
              className={cn('h-2 w-2 shrink-0 rounded-full', n.online ? 'bg-green-500' : 'bg-[var(--color-text-muted)]')}
            />
            <span className="truncate">{label}</span>
            {here && (
              <span className="shrink-0 rounded-full bg-[var(--color-surface-alt)] px-2 py-0.5 text-xs font-normal text-[var(--color-text-muted)]">
                This computer
              </span>
            )}
          </p>
          <p className="text-xs text-[var(--color-text-muted)]">
            {n.online ? 'Online' : `Offline${n.last_seen ? ` · last seen ${timeAgo(n.last_seen)}` : ''}`}
            {' · '}
            <span data-testid="extension-state" className={cn(accessOff && 'text-amber-500')}>
              {accessOff ? 'Website access is off' : extensionLabel(ext)}
            </span>
          </p>
          <p className="text-xs text-[var(--color-text-muted)]">{SETTINGS_NOTE}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {!n.online && (
            <a href={AGENT_START_URL} className={linkButton} title="Works on the computer where the agent is installed">
              <Play size={14} aria-hidden="true" />
              Start
            </a>
          )}
          {maySignOut && !confirming && (
            <button type="button" className={linkButton} onClick={() => setConfirming(true)}>
              <LogOut size={14} aria-hidden="true" />
              Sign out
            </button>
          )}
        </div>
      </div>
      {accessOff && (
        <div
          data-testid="website-access-off"
          className="mt-2 rounded-lg bg-amber-500/10 p-3 text-sm text-amber-700 dark:text-amber-300"
        >
          <p className="font-medium">Website access is off in {label}'s browser.</p>
          <WebsiteAccessSteps families={websiteAccessFamilies(String(n.capabilities?.browser_website_access_off_in ?? ''))} />
        </div>
      )}
      {confirming && (
        <div role="group" aria-label={`Sign out ${label}`} className="mt-2 rounded-lg border border-[var(--color-border)] p-3 text-sm">
          <p className="text-[var(--color-text)]">
            Sign {label} out of Jarvis Automation? It stops working with Jarvis until someone signs in on it again. Its
            settings on that computer are kept.
          </p>
          {signOut.isError && (
            <p role="alert" className="mt-2 text-red-500">
              {errorMessage(signOut.error, 'Could not sign it out')}
            </p>
          )}
          <div className="mt-2 flex gap-2">
            <button type="button" className={dangerButton} disabled={signOut.isPending} onClick={() => signOut.mutate()}>
              {signOut.isPending ? 'Signing out...' : 'Sign out'}
            </button>
            <button type="button" className={linkButton} onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </li>
  )
}

/**
 * ThisComputer says which of the member's agents runs here (GET /api/addons/this-device). The
 * server matches the page's TCP peer address with each agent's last heartbeat address; it is a
 * hint for display and chat's default, and grants nothing.
 */
function ThisComputer({ device, cantTell, os }: { device: NodeLiveness | null; cantTell: boolean; os: keyof typeof OS_NAME }) {
  if (device) {
    return (
      <p className="text-sm text-[var(--color-text)]" data-testid="this-computer">
        This computer is <strong>{nodeLabel(device)}</strong>
        {device.online ? ': the agent is running.' : ': the agent is installed but not running.'}
      </p>
    )
  }
  return (
    <p className="text-sm text-[var(--color-text-muted)]" data-testid="this-computer">
      {cantTell
        ? `We can't tell which of your computers this is. If the agent isn't on it yet, download it for ${OS_NAME[os]}.`
        : `No Jarvis agent on this computer yet. Download it for ${OS_NAME[os]}.`}
    </p>
  )
}

/**
 * AddonsPage is where a member adds Jarvis to their computers, browsers and phone (install plan
 * §3.2). Minimal for v1: device status from cc's node list (scoped to the caller's households),
 * download links, store links (no extension detection, decided) and pairing approval.
 */
export default function AddonsPage() {
  const { state, households } = useAuth()
  const hereQ = useQuery({
    queryKey: ['this-device'],
    queryFn: () => getThisDevice(),
    refetchInterval: DEVICE_POLL_MS,
    retry: false,
  })
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
  const hereId = hereQ.data?.node_id ?? ''
  const here = devices.find((n) => n.node_id === hereId) ?? null
  const cantTell = hereQ.isError || (hereQ.data?.node_id == null && hereQ.data?.reason !== 'no_agents')

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <div>
        <h1 className="text-xl font-semibold text-[var(--color-text)]">Add-ons</h1>
        <p className="text-sm text-[var(--color-text-muted)]">
          Use Jarvis on your computers, in your browser and on your phone.
        </p>
      </div>

      <Section title="This computer" icon={<Monitor size={18} aria-hidden="true" />}>
        {hereQ.isLoading || nodesQ.isLoading ? (
          <p className="text-sm text-[var(--color-text-muted)]">Loading...</p>
        ) : (
          <ThisComputer device={here} cantTell={cantTell} os={os} />
        )}
        {here && !here.online && (
          <div className="mt-3">
            <a href={AGENT_START_URL} className={linkButton}>
              <Play size={14} aria-hidden="true" />
              Start the agent
            </a>
          </div>
        )}
      </Section>

      <Section title="Desktop agent" icon={<Laptop size={18} aria-hidden="true" />}>
        <p className="mb-3 text-sm text-[var(--color-text-muted)]">
          Talk to Jarvis with "Hey Jarvis" on a computer, and let it open apps, read the screen and type for you.
        </p>
        {/* Through jarvisd, verified against the release signature (install plan 3.1). */}
        <AgentDownload size="sm" />
        <div className="mt-3 flex flex-wrap gap-2">
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
              <DeviceRow key={n.node_id} n={n} here={n.node_id === hereId} maySignOut={canSignOut(n, state.user?.id, households)} />
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
          {LINKS.chromeStore && <External href={LINKS.chromeStore}>Add to Chrome</External>}
          {!LINKS.firefoxStore && !LINKS.chromeStore && (
            <>
              <p className="w-full text-sm text-[var(--color-text-muted)]">
                The store listings are on their way. Until then the desktop agent sets it up.
              </p>
              <External href={LINKS.browserGuide}>How browser control works</External>
            </>
          )}
        </div>
        <div className="mt-3 text-sm text-[var(--color-text-muted)]">
          <p className="font-medium text-[var(--color-text)]">After installing, turn on website access</p>
          <WebsiteAccessSteps families={['firefox', 'chrome']} />
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

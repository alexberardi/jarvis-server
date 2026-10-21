import { useState } from 'react'
import type { ReactNode } from 'react'
import { ExternalLink, Laptop, LogOut, Monitor, Play, Puzzle, Smartphone } from 'lucide-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getThisDevice } from '@/api/chat'
import { extensionState, getNodeLiveness, signOutDevice, websiteAccessState } from '@/api/nodes'
import type { NodeLiveness } from '@/api/nodes'
import AgentDownload from '@/components/addons/AgentDownload'
import PendingPairings from '@/components/nodes/PendingPairings'
import {
  Badge,
  Button,
  buttonStyles,
  Card,
  CardHeader,
  EmptyState,
  ErrorState,
  PageHeader,
  Skeleton,
} from '@/components/ui'
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

/** AddonCard is one add-on: a card with an icon heading (a labelled region). */
function AddonCard({ id, title, icon, description, children }: { id: string; title: string; icon: ReactNode; description?: ReactNode; children: ReactNode }) {
  return (
    <Card as="section" aria-labelledby={id}>
      <CardHeader
        id={id}
        title={title}
        icon={icon}
        description={description}
      />
      {children}
    </Card>
  )
}

function External({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" className={buttonStyles({ size: 'sm' })}>
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
      <ul className="my-1.5 list-disc space-y-1 pl-5">
        {families.map((f) => (
          <li key={f}>{WEBSITE_ACCESS[f]}</li>
        ))}
      </ul>
      <p>{WEBSITE_ACCESS.reload}</p>
      <div className="mt-3">
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
    <li className="py-3 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
        <div className="min-w-0 flex-1">
          <p className="flex min-w-0 items-center gap-2 text-subheading text-text">
            <span
              aria-hidden="true"
              className={cn('size-2 shrink-0 rounded-pill', n.online ? 'bg-success' : 'bg-text-muted')}
            />
            <span className="truncate">{label}</span>
            {here && <Badge>This computer</Badge>}
          </p>
          <p className="text-small text-text-muted">
            {n.online ? 'Online' : `Offline${n.last_seen ? ` · last seen ${timeAgo(n.last_seen)}` : ''}`}
            {' · '}
            <span data-testid="extension-state" className={cn(accessOff && 'text-warning')}>
              {accessOff ? 'Website access is off' : extensionLabel(ext)}
            </span>
          </p>
          <p className="text-small text-text-muted">{SETTINGS_NOTE}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {!n.online && (
            <a
              href={AGENT_START_URL}
              className={buttonStyles({ size: 'sm' })}
              title="Works on the computer where the agent is installed"
            >
              <Play size={14} aria-hidden="true" />
              Start
            </a>
          )}
          {maySignOut && !confirming && (
            <Button size="sm" variant="ghost" icon={<LogOut size={14} aria-hidden="true" />} onClick={() => setConfirming(true)}>
              Sign out
            </Button>
          )}
        </div>
      </div>
      {accessOff && (
        <div data-testid="website-access-off" className="mt-3 rounded-control bg-warning-soft p-3 text-small text-text">
          <p className="font-medium text-warning">Website access is off in {label}'s browser.</p>
          <WebsiteAccessSteps families={websiteAccessFamilies(String(n.capabilities?.browser_website_access_off_in ?? ''))} />
        </div>
      )}
      {confirming && (
        <div role="group" aria-label={`Sign out ${label}`} className="mt-3 rounded-control bg-surface-alt p-3 text-small">
          <p className="text-text">
            Sign {label} out of Jarvis Automation? It stops working with Jarvis until someone signs in on it again. Its
            settings on that computer are kept.
          </p>
          {signOut.isError && (
            <p role="alert" className="mt-2 text-danger">
              {errorMessage(signOut.error, 'Could not sign it out. Try again.')}
            </p>
          )}
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="danger" loading={signOut.isPending} onClick={() => signOut.mutate()}>
              Sign out
            </Button>
            <Button size="sm" onClick={() => setConfirming(false)}>
              Cancel
            </Button>
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
      <p className="text-body text-text" data-testid="this-computer">
        This computer is <strong>{nodeLabel(device)}</strong>
        {device.online ? ': the agent is running.' : ': the agent is installed but not running.'}
      </p>
    )
  }
  return (
    <p className="text-body text-text-muted" data-testid="this-computer">
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

  const loadingLine = (label: string) => (
    <div role="status" aria-label={label} className="space-y-2">
      <Skeleton className="w-3/5" />
      <Skeleton className="w-2/5" />
    </div>
  )

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title="Add-ons" description="Use Jarvis on your computers, in your browser and on your phone." />
      <div className="space-y-4">
        <AddonCard id="addon-this-computer" title="This computer" icon={<Monitor size={18} />}>
          {hereQ.isLoading || nodesQ.isLoading ? (
            loadingLine('Checking this computer')
          ) : (
            <ThisComputer device={here} cantTell={cantTell} os={os} />
          )}
          {here && !here.online && (
            <div className="mt-3">
              <a href={AGENT_START_URL} className={buttonStyles({ variant: 'primary', size: 'sm' })}>
                <Play size={14} aria-hidden="true" />
                Start the agent
              </a>
            </div>
          )}
        </AddonCard>

        <AddonCard
          id="addon-desktop-agent"
          title="Desktop agent"
          icon={<Laptop size={18} />}
          description='Talk to Jarvis with "Hey Jarvis" on a computer, and let it open apps, read the screen and type for you.'
        >
          {/* Through jarvisd, verified against the release signature (install plan 3.1). */}
          <AgentDownload size="sm" />
          <div className="mt-3 flex flex-wrap gap-2">
            <External href={LINKS.agentGuide}>Install guide</External>
          </div>
        </AddonCard>

        <AddonCard id="addon-your-devices" title="Your devices" icon={<Laptop size={18} />}>
          {nodesQ.isError ? (
            <ErrorState
              className="py-4"
              title="Couldn't load your devices"
              message={errorMessage(nodesQ.error, 'The server did not answer.')}
              onRetry={() => nodesQ.refetch()}
            />
          ) : nodesQ.isLoading ? (
            loadingLine('Loading your devices')
          ) : devices.length === 0 ? (
            <EmptyState
              className="py-4"
              icon={<Laptop size={20} />}
              title="No computers yet"
              description="Install the desktop agent, then approve the code it shows under Pending pairings."
            />
          ) : (
            <ul className="divide-y divide-border" aria-label="Your devices">
              {devices.map((n) => (
                <DeviceRow key={n.node_id} n={n} here={n.node_id === hereId} maySignOut={canSignOut(n, state.user?.id, households)} />
              ))}
            </ul>
          )}
        </AddonCard>

        <PendingPairings systemInfo={Boolean(state.user?.is_superuser)} />

        <AddonCard
          id="addon-browser-extension"
          title="Browser extension"
          icon={<Puzzle size={18} />}
          description="Lets Jarvis read and control tabs in your browser, through the desktop agent on the same computer."
        >
          <div className="flex flex-wrap items-center gap-2">
            {LINKS.firefoxStore && <External href={LINKS.firefoxStore}>Add to Firefox</External>}
            {LINKS.chromeStore && <External href={LINKS.chromeStore}>Add to Chrome</External>}
            {!LINKS.firefoxStore && !LINKS.chromeStore && (
              <>
                <p className="w-full text-small text-text-muted">
                  The store listings are on their way. Until then the desktop agent sets it up.
                </p>
                <External href={LINKS.browserGuide}>How browser control works</External>
              </>
            )}
          </div>
          <div className="mt-4 border-t border-border pt-4 text-small text-text-muted">
            <h3 className="text-subheading text-text">After installing, turn on website access</h3>
            <WebsiteAccessSteps families={['firefox', 'chrome']} />
          </div>
        </AddonCard>

        <AddonCard
          id="addon-phone-app"
          title="Phone app"
          icon={<Smartphone size={18} />}
          description="Chat, voice, your inbox and household settings on iPhone and Android."
        >
          <div className="flex flex-wrap gap-2">
            {LINKS.appStore && <External href={LINKS.appStore}>App Store</External>}
            {LINKS.playStore && <External href={LINKS.playStore}>Google Play</External>}
            {!LINKS.appStore && !LINKS.playStore && (
              <p className="text-small text-text-muted">
                The Jarvis Automation app is in testing. Ask whoever set up Jarvis Automation for an invite.
              </p>
            )}
          </div>
        </AddonCard>
      </div>
    </div>
  )
}

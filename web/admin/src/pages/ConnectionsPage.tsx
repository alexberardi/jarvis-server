import { useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AppWindow, Check, Copy, Globe, KeyRound, Link2, Pencil, Plus, RefreshCw, Server, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import {
  addService,
  createApp,
  getConnections,
  publicURLError,
  removeService,
  revokeApp,
  rotateApp,
  setPublicURL,
  type AppKey,
  type ConnectionsResponse,
  type HealthStatus,
} from '@/api/connections'
import {
  Badge,
  Button,
  Card,
  CardHeader,
  Dialog,
  EmptyState,
  ErrorState,
  Input,
  PageHeader,
  SkeletonList,
} from '@/components/ui'
import { errorMessage } from '@/lib/errors'

const KEYS = {
  quick: ['connections', 'no-health'] as const,
  probed: ['connections', 'health'] as const,
}

function HealthBadge({ health, probing }: { health: HealthStatus | null | undefined; probing: boolean }) {
  if (!health) return <Badge>{probing ? 'checking…' : 'unknown'}</Badge>
  if (health.healthy) {
    return (
      <Badge tone="success" dot>
        healthy{health.latency_ms !== undefined ? ` · ${Math.round(health.latency_ms)} ms` : ''}
      </Badge>
    )
  }
  return (
    <Badge tone="danger" title={health.error}>
      unreachable
    </Badge>
  )
}

/**
 * KeyModal shows an app key exactly once (I5). Closing it drops the key from memory; there is
 * no way to see it again, only to rotate. Only "I've stored it" closes it, so a stray tap on
 * the backdrop can't lose the key.
 */
export function KeyModal({ appKey, onClose }: { appKey: AppKey; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  return (
    <Dialog
      open
      onClose={onClose}
      dismissible={false}
      title={<span className="break-all">Key for {appKey.app_id}</span>}
      description="Copy it now. This is the only time it is shown; if it is lost, rotate the key to get a new one."
      footer={
        <Button variant="primary" onClick={onClose}>
          I've stored it
        </Button>
      }
    >
      <div className="space-y-3">
        <div className="flex items-start gap-2">
          <code
            data-testid="app-key"
            className="min-w-0 flex-1 rounded-control bg-surface-alt px-3 py-2 font-mono text-small break-all text-text"
          >
            {appKey.app_key}
          </code>
          <Button
            size="sm"
            className="mt-1"
            icon={copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
            onClick={() => {
              void navigator.clipboard?.writeText(appKey.app_key).then(
                () => setCopied(true),
                () => {},
              )
            }}
          >
            {copied ? 'Copied' : 'Copy'}
          </Button>
        </div>
        <p className="text-small text-text-muted">
          The app sends it as <code className="font-mono">X-Jarvis-App-Id</code> +{' '}
          <code className="font-mono">X-Jarvis-App-Key</code>.
        </p>
      </div>
    </Dialog>
  )
}

/** Shown wherever a public URL is entered. */
const PUBLIC_URL_HINT =
  'Only for access from outside your network through a tunnel or reverse proxy, e.g. a Cloudflare tunnel: the public hostname that reaches this service, like https://command-center.example.io, or wss://mqtt.example.io for the MQTT broker. Clients that come in through a public hostname get these URLs; clients on your network keep the local ones.'

/**
 * PublicURLField shows a row's public base URL and edits it in place. Saving an empty value
 * clears it.
 */
export function PublicURLField({ name, value }: { name: string; value: string | null }) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(value ?? '')
  const problem = publicURLError(draft)
  const save = useMutation({
    mutationFn: (v: string | null) => setPublicURL(name, v),
    onSuccess: (r) => {
      toast.success(r.public_url ? `${name}: public URL saved` : `${name}: public URL cleared`)
      void qc.invalidateQueries({ queryKey: ['connections'] })
      setEditing(false)
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not save the public URL')),
  })
  if (!editing) {
    return (
      <div className="flex min-w-0 items-center gap-1.5 text-small text-text-muted">
        <Globe size={14} aria-hidden className="shrink-0" />
        {value ? (
          <span data-testid={`public-url-${name}`} className="min-w-0 break-all text-text">
            {value}
          </span>
        ) : (
          <span>no public URL</span>
        )}
        <Button
          variant="ghost"
          size="sm"
          className="-my-1 px-2"
          title={`Edit the public URL of ${name}`}
          aria-label={`Edit the public URL of ${name}`}
          icon={<Pencil size={14} aria-hidden />}
          onClick={() => {
            setDraft(value ?? '')
            setEditing(true)
          }}
        >
          Edit
        </Button>
      </div>
    )
  }
  return (
    <form
      className="mt-2 w-full"
      onSubmit={(e) => {
        e.preventDefault()
        if (!problem) save.mutate(draft.trim() || null)
      }}
    >
      <div className="flex flex-wrap items-start gap-2">
        <Input
          aria-label={`Public URL for ${name}`}
          wrapperClassName="min-w-0 flex-1 basis-60"
          placeholder="https://jarvis.example.com"
          value={draft}
          error={problem ? <span role="alert">{problem}</span> : undefined}
          hint={PUBLIC_URL_HINT}
          onChange={(e) => setDraft(e.target.value)}
        />
        <div className="flex gap-2">
          <Button onClick={() => setEditing(false)}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!!problem} loading={save.isPending}>
            {draft.trim() ? 'Save' : 'Clear'}
          </Button>
        </div>
      </div>
    </form>
  )
}

/** InlineForm is a form that opens inside a card (add a service, create an app): a tinted panel, not a box in a box. */
function InlineForm({ onSubmit, children }: { onSubmit: () => void; children: ReactNode }) {
  return (
    <form
      className="mb-4 grid gap-3 rounded-control bg-surface-alt p-3 sm:grid-cols-2 md:p-4"
      onSubmit={(e) => {
        e.preventDefault()
        onSubmit()
      }}
    >
      {children}
    </form>
  )
}

function AddServiceForm({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [healthPath, setHealthPath] = useState('/health')
  const [description, setDescription] = useState('')
  const [publicURL, setPublicURLDraft] = useState('')
  const publicProblem = publicURLError(publicURL)
  const add = useMutation({
    mutationFn: () =>
      addService({
        name: name.trim(),
        url: url.trim(),
        health_path: healthPath.trim(),
        description: description.trim(),
        public_url: publicURL.trim() || undefined,
      }),
    onSuccess: () => {
      toast.success(`Added ${name.trim()}`)
      void qc.invalidateQueries({ queryKey: ['connections'] })
      onDone()
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not add the service')),
  })
  return (
    <InlineForm onSubmit={() => add.mutate()}>
      <Input label="Service name" placeholder="jarvis-recipes" value={name} onChange={(e) => setName(e.target.value)} />
      <Input label="Base URL" placeholder="http://host:port" value={url} onChange={(e) => setUrl(e.target.value)} />
      <Input label="Health path" placeholder="/health" value={healthPath} onChange={(e) => setHealthPath(e.target.value)} />
      <Input label="Description" hint="Optional." value={description} onChange={(e) => setDescription(e.target.value)} />
      <Input
        label="Public URL"
        wrapperClassName="sm:col-span-2"
        placeholder="https://recipes.example.io"
        hint="Optional, for access through a tunnel or reverse proxy."
        error={publicProblem ? <span role="alert">{publicProblem}</span> : undefined}
        value={publicURL}
        onChange={(e) => setPublicURLDraft(e.target.value)}
      />
      <div className="flex justify-end gap-2 sm:col-span-2">
        <Button onClick={onDone}>Cancel</Button>
        <Button type="submit" variant="primary" disabled={!name.trim() || !url.trim() || !!publicProblem} loading={add.isPending}>
          Add
        </Button>
      </div>
    </InlineForm>
  )
}

function CreateAppForm({ onCreated, onCancel }: { onCreated: (k: AppKey) => void; onCancel: () => void }) {
  const qc = useQueryClient()
  const [appId, setAppId] = useState('')
  const [name, setName] = useState('')
  const create = useMutation({
    mutationFn: () => createApp(appId.trim(), name.trim()),
    onSuccess: (k) => {
      void qc.invalidateQueries({ queryKey: ['connections'] })
      onCreated(k)
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not create the app client')),
  })
  return (
    <InlineForm onSubmit={() => create.mutate()}>
      <Input label="App ID" placeholder="jarvis-recipes" value={appId} onChange={(e) => setAppId(e.target.value)} />
      <Input label="App name" placeholder="Recipes" value={name} onChange={(e) => setName(e.target.value)} />
      <div className="flex justify-end gap-2 sm:col-span-2">
        <Button onClick={onCancel}>Cancel</Button>
        <Button type="submit" variant="primary" disabled={!appId.trim() || !name.trim()} loading={create.isPending}>
          Create
        </Button>
      </div>
    </InlineForm>
  )
}

/** ConnectionCard is one of the page's three sections: a card with an icon heading. */
function ConnectionCard({
  id,
  title,
  icon,
  description,
  actions,
  children,
}: {
  id: string
  title: string
  icon: ReactNode
  description: ReactNode
  actions?: ReactNode
  children: ReactNode
}) {
  return (
    <Card as="section" aria-labelledby={id}>
      <CardHeader
        id={id}
        title={
          <span className="flex items-center gap-2">
            <span className="text-text-muted">{icon}</span>
            {title}
          </span>
        }
        description={description}
        actions={actions}
      />
      {children}
    </Card>
  )
}

/** Row is one connection: its name and badges, the lines under it, and its health or actions at the right. */
function Row({ testId, title, meta, children, trailing }: { testId: string; title: ReactNode; meta?: ReactNode; children?: ReactNode; trailing?: ReactNode }) {
  return (
    <li className="flex flex-wrap items-start justify-between gap-x-3 gap-y-2 py-3 first:pt-0 last:pb-0" data-testid={testId}>
      <div className="min-w-0 flex-1 basis-64 space-y-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">{title}</div>
        {meta && <p className="text-small break-all text-text-muted">{meta}</p>}
        {children}
      </div>
      {trailing && <div className="flex flex-wrap items-center gap-2">{trailing}</div>}
    </li>
  )
}

const rowName = 'font-mono text-subheading break-all text-text'

/**
 * ConnectionsPage (AD7): what talks to jarvisd. The list renders at once without health
 * (`?health=false`), then a second request probes every row and fills the health in.
 */
export default function ConnectionsPage() {
  const qc = useQueryClient()
  const quick = useQuery<ConnectionsResponse>({ queryKey: KEYS.quick, queryFn: () => getConnections(false) })
  const probed = useQuery<ConnectionsResponse>({ queryKey: KEYS.probed, queryFn: () => getConnections(true), staleTime: 15_000 })
  const data = probed.data ?? quick.data
  const probing = probed.isFetching

  const [adding, setAdding] = useState(false)
  const [creating, setCreating] = useState(false)
  const [shownKey, setShownKey] = useState<AppKey | null>(null)

  const refresh = () => void qc.invalidateQueries({ queryKey: ['connections'] })

  const remove = useMutation({
    mutationFn: (name: string) => removeService(name),
    onSuccess: (_, name) => {
      toast.success(`Removed ${name}`)
      refresh()
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not remove it')),
  })
  const rotate = useMutation({
    mutationFn: (appId: string) => rotateApp(appId),
    onSuccess: (k) => {
      setShownKey(k)
      refresh()
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not rotate the key')),
  })
  const revoke = useMutation({
    mutationFn: (appId: string) => revokeApp(appId),
    onSuccess: (_, appId) => {
      toast.success(`Revoked ${appId}`)
      refresh()
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not revoke it')),
  })

  const header = (
    <PageHeader
      title="Connections"
      description="jarvisd's ports, the services it knows about, and the apps allowed to call it."
      actions={
        <Button size="sm" icon={<RefreshCw size={16} aria-hidden />} loading={probing} onClick={refresh}>
          Check again
        </Button>
      }
    />
  )

  if (quick.isLoading && !data) {
    return (
      <div className="mx-auto max-w-5xl">
        {header}
        <Card flush>
          <SkeletonList rows={6} label="Loading connections" />
        </Card>
      </div>
    )
  }
  if (!data) {
    return (
      <div className="mx-auto max-w-5xl">
        {header}
        <Card>
          <ErrorState
            title="Couldn't load the connections"
            message={errorMessage(quick.error, 'The server did not answer. Check that Jarvis Automation is running.')}
            onRetry={() => quick.refetch()}
          />
        </Card>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-5xl">
      {header}
      <div className="space-y-4">
        <ConnectionCard
          id="conn-listeners"
          title="jarvisd listeners"
          icon={<Server size={18} aria-hidden />}
          description="Managed by jarvisd; change ports with JARVIS_PORT_* in jarvisd.env. Behind a tunnel (e.g. Cloudflare), set each public hostname as that listener's public URL."
        >
          <ul className="divide-y divide-border">
            {data.listeners.map((l) => (
              <Row
                key={l.name}
                testId={`listener-${l.name}`}
                title={
                  <>
                    <code className={rowName}>{l.name}</code>
                    {l.managed === 'broker' && <Badge>MQTT</Badge>}
                  </>
                }
                meta={l.url}
                trailing={<HealthBadge health={l.health} probing={probing} />}
              >
                <PublicURLField name={l.name} value={l.public_url} />
              </Row>
            ))}
          </ul>
        </ConnectionCard>

        <ConnectionCard
          id="conn-external"
          title="External services"
          icon={<Link2 size={18} aria-hidden />}
          description="Add-ons and other machines registered with jarvisd's service registry, so nodes and apps can find them."
          actions={
            !adding && (
              <Button size="sm" icon={<Plus size={16} aria-hidden />} onClick={() => setAdding(true)}>
                Add
              </Button>
            )
          }
        >
          {adding && <AddServiceForm onDone={() => setAdding(false)} />}
          {data.external.length === 0 ? (
            !adding && (
              <EmptyState
                className="py-6"
                title="No external services"
                description="Add one when an add-on or another machine should be found by nodes and apps."
              />
            )
          ) : (
            <ul className="divide-y divide-border">
              {data.external.map((x) => (
                <Row
                  key={x.name}
                  testId={`external-${x.name}`}
                  title={
                    <>
                      <code className={rowName}>{x.name}</code>
                      {x.managed === 'setting' && <Badge title="Follows a jarvisd setting; change it under Settings">from settings</Badge>}
                    </>
                  }
                  meta={
                    <>
                      {x.url}
                      {x.description ? ` · ${x.description}` : ''}
                    </>
                  }
                  trailing={
                    <>
                      <HealthBadge health={x.health} probing={probing} />
                      {x.removable && (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="px-2"
                          title={`Remove ${x.name}`}
                          aria-label={`Remove ${x.name}`}
                          icon={<Trash2 size={14} aria-hidden />}
                          disabled={remove.isPending}
                          onClick={() => {
                            if (window.confirm(`Remove ${x.name} from the registry?`)) remove.mutate(x.name)
                          }}
                        >
                          Remove
                        </Button>
                      )}
                    </>
                  }
                >
                  <PublicURLField name={x.name} value={x.public_url} />
                </Row>
              ))}
            </ul>
          )}
        </ConnectionCard>

        <ConnectionCard
          id="conn-apps"
          title="App clients"
          icon={<AppWindow size={18} aria-hidden />}
          description="Credentials for other programs that call jarvisd (add-ons, a remote GPU box). Phones and nodes don't need one."
          actions={
            !creating && (
              <Button size="sm" icon={<Plus size={16} aria-hidden />} onClick={() => setCreating(true)}>
                New app
              </Button>
            )
          }
        >
          {creating && (
            <CreateAppForm
              onCancel={() => setCreating(false)}
              onCreated={(k) => {
                setCreating(false)
                setShownKey(k)
              }}
            />
          )}
          {data.apps.length === 0 ? (
            !creating && (
              <EmptyState
                className="py-6"
                title="No app clients"
                description="Create one for a program that calls jarvisd from another machine. It gets a key, shown once."
              />
            )
          ) : (
            <ul className="divide-y divide-border">
              {data.apps.map((a) => (
                <Row
                  key={a.app_id}
                  testId={`app-${a.app_id}`}
                  title={
                    <>
                      <code className={rowName}>{a.app_id}</code>
                      <span className="text-body text-text-muted">{a.name}</span>
                      {a.is_active ? <Badge tone="success">active</Badge> : <Badge tone="danger">revoked</Badge>}
                    </>
                  }
                  meta={
                    <>
                      Created {new Date(a.created_at).toLocaleDateString()}
                      {a.last_rotated_at ? ` · key rotated ${new Date(a.last_rotated_at).toLocaleDateString()}` : ''}
                    </>
                  }
                  trailing={
                    <>
                      <Button
                        size="sm"
                        icon={<KeyRound size={14} aria-hidden />}
                        disabled={rotate.isPending}
                        onClick={() => {
                          if (window.confirm(`Issue a new key for ${a.app_id}? The old key stops working.`)) rotate.mutate(a.app_id)
                        }}
                      >
                        {a.is_active ? 'Rotate key' : 'Reissue key'}
                      </Button>
                      {a.is_active && (
                        <Button
                          size="sm"
                          variant="danger"
                          disabled={revoke.isPending}
                          onClick={() => {
                            if (window.confirm(`Revoke ${a.app_id}? Its key stops working at once.`)) revoke.mutate(a.app_id)
                          }}
                        >
                          Revoke
                        </Button>
                      )}
                    </>
                  }
                />
              ))}
            </ul>
          )}
        </ConnectionCard>
      </div>

      {shownKey && <KeyModal appKey={shownKey} onClose={() => setShownKey(null)} />}
    </div>
  )
}

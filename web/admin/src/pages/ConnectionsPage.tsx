import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AppWindow, Check, Copy, KeyRound, Link2, Plus, RefreshCw, Server, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import {
  addService,
  createApp,
  getConnections,
  removeService,
  revokeApp,
  rotateApp,
  type AppKey,
  type ConnectionsResponse,
  type HealthStatus,
} from '@/api/connections'
import { buttonClass, inputClass } from '@/components/models/styles'
import { Pill, Section } from '@/components/models/ui'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

const KEYS = {
  quick: ['connections', 'no-health'] as const,
  probed: ['connections', 'health'] as const,
}

function HealthPill({ health, probing }: { health: HealthStatus | null | undefined; probing: boolean }) {
  if (!health) return <Pill tone="muted">{probing ? 'checking…' : 'unknown'}</Pill>
  if (health.healthy) {
    return <Pill tone="ok">healthy{health.latency_ms !== undefined ? ` · ${Math.round(health.latency_ms)} ms` : ''}</Pill>
  }
  return (
    <Pill tone="bad" title={health.error}>
      unreachable
    </Pill>
  )
}

/**
 * KeyModal shows an app key exactly once (I5). Closing it drops the key from memory; there is
 * no way to see it again, only to rotate.
 */
export function KeyModal({ appKey, onClose }: { appKey: AppKey; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" role="dialog" aria-modal="true" aria-labelledby="key-title">
      <div className="w-full max-w-lg space-y-4 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-xl">
        <h2 id="key-title" className="flex items-center gap-2 text-lg font-semibold text-[var(--color-text)]">
          <KeyRound size={18} /> Key for {appKey.app_id}
        </h2>
        <p className="text-sm text-[var(--color-text-muted)]">
          Copy it now. This is the only time it is shown; if it is lost, rotate the key to get a new one. The app sends it
          as <code>X-Jarvis-App-Id</code> + <code>X-Jarvis-App-Key</code>.
        </p>
        <div className="flex items-center gap-2">
          <code data-testid="app-key" className="min-w-0 flex-1 break-all rounded bg-[var(--color-surface-alt)] px-2 py-1.5 font-mono text-xs text-[var(--color-text)]">
            {appKey.app_key}
          </code>
          <button
            type="button"
            className={buttonClass.secondary}
            onClick={() => {
              void navigator.clipboard?.writeText(appKey.app_key).then(
                () => setCopied(true),
                () => {},
              )
            }}
          >
            {copied ? <Check size={12} /> : <Copy size={12} />} {copied ? 'Copied' : 'Copy'}
          </button>
        </div>
        <div className="flex justify-end">
          <button type="button" className={buttonClass.primary} onClick={onClose}>
            I've stored it
          </button>
        </div>
      </div>
    </div>
  )
}

function AddServiceForm({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [healthPath, setHealthPath] = useState('/health')
  const [description, setDescription] = useState('')
  const add = useMutation({
    mutationFn: () => addService({ name: name.trim(), url: url.trim(), health_path: healthPath.trim(), description: description.trim() }),
    onSuccess: () => {
      toast.success(`Added ${name.trim()}`)
      void qc.invalidateQueries({ queryKey: ['connections'] })
      onDone()
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not add the service')),
  })
  return (
    <form
      className="grid gap-2 rounded-lg border border-[var(--color-border)] p-3 sm:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault()
        add.mutate()
      }}
    >
      <input aria-label="Service name" className={inputClass} placeholder="Name, e.g. jarvis-recipes" value={name} onChange={(e) => setName(e.target.value)} />
      <input aria-label="Base URL" className={inputClass} placeholder="http://host:port" value={url} onChange={(e) => setUrl(e.target.value)} />
      <input aria-label="Health path" className={inputClass} placeholder="/health" value={healthPath} onChange={(e) => setHealthPath(e.target.value)} />
      <input aria-label="Description" className={inputClass} placeholder="Description (optional)" value={description} onChange={(e) => setDescription(e.target.value)} />
      <div className="flex justify-end gap-2 sm:col-span-2">
        <button type="button" className={buttonClass.secondary} onClick={onDone}>
          Cancel
        </button>
        <button type="submit" className={buttonClass.primary} disabled={!name.trim() || !url.trim() || add.isPending}>
          Add
        </button>
      </div>
    </form>
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
    <form
      className="grid gap-2 rounded-lg border border-[var(--color-border)] p-3 sm:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault()
        create.mutate()
      }}
    >
      <input aria-label="App ID" className={inputClass} placeholder="App ID, e.g. jarvis-recipes" value={appId} onChange={(e) => setAppId(e.target.value)} />
      <input aria-label="App name" className={inputClass} placeholder="Display name" value={name} onChange={(e) => setName(e.target.value)} />
      <div className="flex justify-end gap-2 sm:col-span-2">
        <button type="button" className={buttonClass.secondary} onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" className={buttonClass.primary} disabled={!appId.trim() || !name.trim() || create.isPending}>
          Create
        </button>
      </div>
    </form>
  )
}

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

  if (quick.isLoading && !data) return <p className="text-sm text-[var(--color-text-muted)]">Loading…</p>
  if (!data) return <p className="text-sm text-red-500">{errorMessage(quick.error, 'Could not load the connections')}</p>

  return (
    <div className="mx-auto max-w-5xl space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-[var(--color-text)]">Connections</h1>
          <p className="text-sm text-[var(--color-text-muted)]">jarvisd's ports, the services it knows about, and the apps allowed to call it.</p>
        </div>
        <button type="button" className={buttonClass.icon} title="Check again" aria-label="Check again" onClick={refresh}>
          <RefreshCw size={14} className={cn(probing && 'animate-spin')} />
        </button>
      </div>

      <Section title="jarvisd listeners" icon={Server} description="Managed by jarvisd; change ports with JARVIS_PORT_* in jarvisd.env.">
        <ul className="divide-y divide-[var(--color-border)]">
          {data.listeners.map((l) => (
            <li key={l.name} className="flex flex-wrap items-center justify-between gap-2 py-1.5 text-sm">
              <div className="flex items-center gap-2">
                <code className="text-[var(--color-text)]">{l.name}</code>
                <span className="text-xs text-[var(--color-text-muted)]">{l.url}</span>
                {l.managed === 'broker' && <Pill>MQTT</Pill>}
              </div>
              <HealthPill health={l.health} probing={probing} />
            </li>
          ))}
        </ul>
      </Section>

      <Section
        title="External services"
        icon={Link2}
        description="Add-ons and other machines registered with jarvisd's service registry, so nodes and apps can find them."
        actions={
          !adding && (
            <button type="button" className={buttonClass.secondary} onClick={() => setAdding(true)}>
              <Plus size={12} /> Add
            </button>
          )
        }
      >
        {adding && <AddServiceForm onDone={() => setAdding(false)} />}
        {data.external.length === 0 ? (
          <p className="text-xs text-[var(--color-text-muted)]">None.</p>
        ) : (
          <ul className="divide-y divide-[var(--color-border)]">
            {data.external.map((x) => (
              <li key={x.name} className="flex flex-wrap items-center justify-between gap-2 py-1.5 text-sm" data-testid={`external-${x.name}`}>
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <code className="text-[var(--color-text)]">{x.name}</code>
                    {x.managed === 'setting' && <Pill title="Follows a jarvisd setting; change it under Settings">from settings</Pill>}
                  </div>
                  <p className="truncate text-xs text-[var(--color-text-muted)]">
                    {x.url}
                    {x.description ? ` · ${x.description}` : ''}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <HealthPill health={x.health} probing={probing} />
                  {x.removable && (
                    <button
                      type="button"
                      className={buttonClass.icon}
                      title={`Remove ${x.name}`}
                      aria-label={`Remove ${x.name}`}
                      disabled={remove.isPending}
                      onClick={() => {
                        if (window.confirm(`Remove ${x.name} from the registry?`)) remove.mutate(x.name)
                      }}
                    >
                      <Trash2 size={14} />
                    </button>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section
        title="App clients"
        icon={AppWindow}
        description="Credentials for other programs that call jarvisd (add-ons, a remote GPU box). Phones and nodes don't need one."
        actions={
          !creating && (
            <button type="button" className={buttonClass.secondary} onClick={() => setCreating(true)}>
              <Plus size={12} /> New app
            </button>
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
          <p className="text-xs text-[var(--color-text-muted)]">None.</p>
        ) : (
          <ul className="divide-y divide-[var(--color-border)]">
            {data.apps.map((a) => (
              <li key={a.app_id} className="flex flex-wrap items-center justify-between gap-2 py-1.5 text-sm" data-testid={`app-${a.app_id}`}>
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <code className="text-[var(--color-text)]">{a.app_id}</code>
                    <span className="text-[var(--color-text-muted)]">{a.name}</span>
                    {a.is_active ? <Pill tone="ok">active</Pill> : <Pill tone="bad">revoked</Pill>}
                  </div>
                  <p className="text-xs text-[var(--color-text-muted)]">
                    created {new Date(a.created_at).toLocaleDateString()}
                    {a.last_rotated_at ? ` · key rotated ${new Date(a.last_rotated_at).toLocaleDateString()}` : ''}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    className={buttonClass.secondary}
                    disabled={rotate.isPending}
                    onClick={() => {
                      if (window.confirm(`Issue a new key for ${a.app_id}? The old key stops working.`)) rotate.mutate(a.app_id)
                    }}
                  >
                    <KeyRound size={12} /> {a.is_active ? 'Rotate key' : 'Reissue key'}
                  </button>
                  {a.is_active && (
                    <button
                      type="button"
                      className={buttonClass.danger}
                      disabled={revoke.isPending}
                      onClick={() => {
                        if (window.confirm(`Revoke ${a.app_id}? Its key stops working at once.`)) revoke.mutate(a.app_id)
                      }}
                    >
                      Revoke
                    </button>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Section>

      {shownKey && <KeyModal appKey={shownKey} onClose={() => setShownKey(null)} />}
    </div>
  )
}

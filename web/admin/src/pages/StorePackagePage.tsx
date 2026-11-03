import { useState } from 'react'
import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, ArrowLeft, Download, ExternalLink, ShieldAlert } from 'lucide-react'
import { useNavigate, useParams } from 'react-router-dom'
import { getStorePackage } from '@/api/store'
import type { StoreDetail } from '@/api/store'
import InstallDialog from '@/components/store/InstallDialog'
import { Badge, Banner, Button, Card, CardHeader, ErrorState, PageHeader, Skeleton, Switch } from '@/components/ui'
import type { BadgeTone } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { useBundle } from '@/hooks/useBundle'
import { errorMessage } from '@/lib/errors'
import { notForComputer } from '@/lib/store'
import { formatServerTime } from '@/lib/time'

/** Pantry's danger rating, 1 to 5, as the app labels it. */
const RISK: Record<number, { label: string; tone: BadgeTone }> = {
  1: { label: 'Very safe', tone: 'success' },
  2: { label: 'Safe', tone: 'success' },
  3: { label: 'Moderate', tone: 'warning' },
  4: { label: 'Risky', tone: 'danger' },
  5: { label: 'Dangerous', tone: 'danger' },
}

const COMPONENT: Record<string, string> = {
  command: 'Command',
  agent: 'Background agent',
  device_protocol: 'Device protocol',
  device_manager: 'Device manager',
  prompt_provider: 'Prompt provider',
}

/**
 * StorePackagePage is one Pantry package in full: what it adds, where it runs, its security
 * review and source, with Install and the default-bundle switch. Looking costs nothing in Pantry;
 * only Install asks for the commit to install (cc's /download, which counts an install).
 */
export default function StorePackagePage() {
  const { name = '' } = useParams<{ name: string }>()
  const { activeHouseholdId: hh } = useAuth()
  const navigate = useNavigate()
  const { bundle, canEdit, toggle } = useBundle()
  const [installing, setInstalling] = useState(false)
  const detail = useQuery({
    queryKey: ['store-package', hh, name],
    queryFn: () => getStorePackage(hh, name),
    enabled: !!hh && !!name,
  })
  const d = detail.data
  const title = d?.display_name || d?.command_name || name

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <Button
        variant="ghost"
        size="sm"
        className="-ml-3"
        icon={<ArrowLeft size={16} aria-hidden />}
        onClick={() => navigate('/admin/store')}
      >
        Back to the Store
      </Button>

      {detail.isError ? (
        <ErrorState
          title={`Couldn't load ${name}`}
          message={errorMessage(detail.error, 'Check your connection and try again.')}
          onRetry={() => detail.refetch()}
        />
      ) : !d ? (
        <div role="status" aria-label="Loading the package" className="space-y-3">
          <Skeleton className="h-8 w-1/2" />
          <Skeleton className="w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      ) : (
        <>
          <PageHeader
            title={title}
            description={d.description}
            actions={
              <div className="flex items-center gap-4">
                {canEdit && (
                  <Switch
                    checked={bundle.includes(d.command_name)}
                    onCheckedChange={(on) => toggle(d.command_name, on)}
                    label="In bundle"
                    aria-label={`${title} in the default bundle`}
                    className="items-center [&>span]:pt-0"
                  />
                )}
                <Button variant="primary" icon={<Download size={16} aria-hidden />} onClick={() => setInstalling(true)}>
                  Install
                </Button>
              </div>
            }
          />

          <Facts d={d} />

          {!!d.apt_packages?.length && (
            <Banner tone="warning" icon={<AlertTriangle size={18} />} title="Installs system packages as root on Pi nodes">
              {d.apt_packages.join(', ')}
            </Banner>
          )}

          {!!d.components?.length && (
            <Card as="section" aria-labelledby="pkg-components">
              <CardHeader id="pkg-components" title="What it adds" className="mb-3" />
              <ul className="divide-y divide-border">
                {d.components.map((c) => (
                  <li key={`${c.type}/${c.name}`} className="flex items-center justify-between gap-3 py-2">
                    <span className="min-w-0 truncate font-mono text-small text-text">{c.name}</span>
                    <Badge>{COMPONENT[c.type] ?? c.type}</Badge>
                  </li>
                ))}
              </ul>
            </Card>
          )}

          {d.security_report && <Security d={d} />}
        </>
      )}

      {installing && d && <InstallDialog names={[d.command_name]} label={title} householdId={hh} onClose={() => setInstalling(false)} />}
    </div>
  )
}

/** Facts is the package's particulars as label and value rows. */
function Facts({ d }: { d: StoreDetail }) {
  const rows: [string, ReactNode][] = [
    ['Version', `v${d.latest_version}`],
    ['Author', d.author?.display_name || d.author?.github || 'Unknown'],
    ['Runs on', notForComputer(d) ? 'Pi nodes only' : 'Pi nodes and computers'],
    ['Type', d.package_type === 'bundle' ? 'Bundle of several parts' : 'Single command'],
  ]
  if (d.categories?.length) rows.push(['Categories', d.categories.join(', ')])
  if (d.platforms?.length) rows.push(['Platforms', d.platforms.join(', ')])
  if (d.license) rows.push(['License', d.license])
  if (d.install_count !== undefined) rows.push(['Installs', d.install_count.toLocaleString()])
  if (d.updated_at) rows.push(['Updated', formatServerTime(d.updated_at, { dateStyle: 'medium' })])
  rows.push([
    'Source',
    <a
      key="src"
      href={d.github_repo_url}
      target="_blank"
      rel="noreferrer"
      className="inline-flex min-w-0 items-center gap-1 text-primary underline underline-offset-2 [overflow-wrap:anywhere]"
    >
      {d.github_repo_url.replace(/^https?:\/\/(www\.)?/, '')}
      <ExternalLink size={14} aria-hidden className="shrink-0" />
    </a>,
  ])
  return (
    <Card as="section" aria-label="About this package">
      <dl className="grid grid-cols-1 items-baseline gap-x-6 sm:grid-cols-[max-content_1fr] sm:gap-y-2">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-small text-text-muted">{k}</dt>
            <dd className="mb-3 min-w-0 text-body text-text sm:mb-0">{v}</dd>
          </div>
        ))}
        {d.verified && (
          <>
            <dt className="text-small text-text-muted">Review</dt>
            <dd>
              <Badge tone="success">Verified by Pantry</Badge>
            </dd>
          </>
        )}
      </dl>
    </Card>
  )
}

/** Security is Pantry's automated security review of the package. */
function Security({ d }: { d: StoreDetail }) {
  const r = d.security_report!
  const risk = RISK[d.danger_rating ?? 0]
  return (
    <Card as="section" aria-labelledby="pkg-security">
      <CardHeader
        id="pkg-security"
        title="Security review"
        icon={<ShieldAlert size={18} />}
        actions={risk && <Badge tone={risk.tone}>{risk.label}</Badge>}
        className="mb-3"
      />
      <p className="max-w-[65ch] text-body text-text">{r.summary}</p>
      {!!r.concerns?.length && (
        <ul className="mt-3 list-disc space-y-1 pl-5 text-small text-text-muted marker:text-text-muted">
          {r.concerns.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
      )}
    </Card>
  )
}

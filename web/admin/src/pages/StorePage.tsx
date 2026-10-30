import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import { Download, Package, PackageOpen, Search } from 'lucide-react'
import { toast } from 'sonner'
import { Link } from 'react-router-dom'
import { getHouseholdSettings, putHouseholdSetting } from '@/api/household'
import { BUNDLE_KEY, browseStore } from '@/api/store'
import type { StorePackage } from '@/api/store'
import InstallDialog from '@/components/store/InstallDialog'
import { Badge, Banner, Button, Card, CardHeader, EmptyState, ErrorState, Input, List, PageHeader, SkeletonList, Switch } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { parseBundle, withBundle } from '@/lib/store'

const ADMIN_ROLES = new Set(['admin', 'owner'])

/** pantryOff: cc's 403 for a household with the Pantry switched off. */
function pantryOff(err: unknown): boolean {
  return isAxiosError(err) && (err.response?.data as { code?: string } | undefined)?.code === 'pantry_disabled'
}

/**
 * StorePage is the Pantry for the active household (decision 2026-10-30-admin-store-page): browse,
 * install a package on the nodes the admin picks, and keep a default bundle that installs together.
 * Nothing installs on its own; a new node gets its packages from here.
 */
export default function StorePage() {
  const { households, activeHouseholdId: hh } = useAuth()
  const qc = useQueryClient()
  const [q, setQ] = useState('')
  const [term, setTerm] = useState('')
  // Ask Pantry once typing pauses, not on every key.
  useEffect(() => {
    const t = setTimeout(() => setTerm(q.trim()), 300)
    return () => clearTimeout(t)
  }, [q])
  const [installing, setInstalling] = useState<{ names: string[]; label: string } | null>(null)
  const household = households.find((h) => h.id === hh)
  const canEdit = !!household && ADMIN_ROLES.has(household.role)

  const list = useQuery({ queryKey: ['store', hh, term], queryFn: () => browseStore(hh, term), enabled: !!hh })
  const settingsKey = ['household-settings', hh]
  const settings = useQuery({ queryKey: settingsKey, queryFn: () => getHouseholdSettings(hh), enabled: !!hh })
  const bundle = parseBundle(settings.data?.[BUNDLE_KEY])
  const setBundle = useMutation({
    mutationFn: (value: string) => putHouseholdSetting(hh, BUNDLE_KEY, value),
    onSuccess: (_, value) => qc.setQueryData<Record<string, unknown>>(settingsKey, (old) => ({ ...old, [BUNDLE_KEY]: value })),
    onError: (err) => toast.error(errorMessage(err, 'Could not change the bundle')),
  })
  const items = list.data ?? []
  const nameOf = (n: string) => items.find((p) => p.command_name === n)?.display_name || n

  if (!hh) {
    return (
      <div className="mx-auto max-w-3xl">
        <PageHeader title="Store" />
        <EmptyState icon={<Package size={20} />} title="You aren't in a household" description="Packages install on a household's nodes." />
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <PageHeader
        title="Store"
        description={`Packages from the Pantry for ${household?.name ?? 'this household'}. Each installs only on the nodes you choose.`}
      />

      {pantryOff(list.error) ? (
        <Banner
          tone="warning"
          title="The Pantry is turned off for this household"
          actions={
            <Link to="/account" className="text-small font-medium text-primary underline underline-offset-2">
              Turn it on in Account
            </Link>
          }
        />
      ) : (
        <>
          <Card as="section" aria-labelledby="store-bundle">
            <CardHeader
              id="store-bundle"
              title="Default bundle"
              icon={<PackageOpen size={18} />}
              description="The packages a new node usually needs, installed together."
              actions={
                <Button
                  variant="primary"
                  icon={<Download size={16} aria-hidden />}
                  disabled={bundle.length === 0}
                  onClick={() => setInstalling({ names: bundle, label: 'the default bundle' })}
                >
                  Install bundle
                </Button>
              }
              className="mb-3"
            />
            {bundle.length ? (
              <ul className="flex flex-wrap gap-2" aria-label="In the bundle">
                {bundle.map((n) => (
                  <li key={n}>
                    <Badge tone="primary">{nameOf(n)}</Badge>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-small text-text-muted">
                {canEdit ? 'Empty. Turn on “In bundle” for a package below.' : 'Empty. A household admin chooses its packages.'}
              </p>
            )}
          </Card>

          <Input
            type="search"
            aria-label="Search packages"
            placeholder="Search packages"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />

          <Card flush className="overflow-hidden">
            {list.isError ? (
              <ErrorState
                title="Couldn't load the Pantry"
                message={errorMessage(list.error, 'Check your connection and try again.')}
                onRetry={() => list.refetch()}
              />
            ) : list.isLoading ? (
              <SkeletonList rows={4} label="Loading packages" />
            ) : items.length === 0 ? (
              <EmptyState icon={<Search size={20} />} title={term ? 'No packages match' : 'The Pantry has no packages'} />
            ) : (
              <List label="Packages">
                {items.map((p) => (
                  <PackageRow
                    key={p.command_name}
                    pkg={p}
                    inBundle={bundle.includes(p.command_name)}
                    canEdit={canEdit}
                    onBundle={(on) => setBundle.mutate(withBundle(bundle, p.command_name, on))}
                    onInstall={() => setInstalling({ names: [p.command_name], label: p.display_name || p.command_name })}
                  />
                ))}
              </List>
            )}
          </Card>
        </>
      )}

      {installing && (
        <InstallDialog names={installing.names} label={installing.label} householdId={hh} onClose={() => setInstalling(null)} />
      )}
    </div>
  )
}

interface PackageRowProps {
  pkg: StorePackage
  inBundle: boolean
  canEdit: boolean
  onBundle: (on: boolean) => void
  onInstall: () => void
}

function PackageRow({ pkg, inBundle, canEdit, onBundle, onInstall }: PackageRowProps) {
  const name = pkg.display_name || pkg.command_name
  return (
    <li className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center" data-testid={`package-${pkg.command_name}`}>
      <div className="min-w-0 flex-1">
        <p className="text-subheading text-text">
          {name} <span className="text-small font-normal text-text-muted">v{pkg.latest_version}</span>
        </p>
        <p className="mt-0.5 line-clamp-2 text-small text-text-muted">{pkg.description}</p>
      </div>
      <div className="flex shrink-0 items-center gap-4">
        {canEdit && (
          <Switch
            checked={inBundle}
            onCheckedChange={onBundle}
            label="In bundle"
            aria-label={`${name} in the default bundle`}
            className="items-center [&>span]:pt-0"
          />
        )}
        <Button size="sm" icon={<Download size={16} aria-hidden />} onClick={onInstall} aria-label={`Install ${name}`}>
          Install
        </Button>
      </div>
    </li>
  )
}

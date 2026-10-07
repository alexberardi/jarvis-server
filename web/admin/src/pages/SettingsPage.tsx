import { useState, useMemo } from 'react'
import { RefreshCw, Search } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useAllSettings } from '@/hooks/useSettings'
import ServiceCard from '@/components/settings/ServiceCard'
import { errorMessage } from '@/lib/errors'
import { withoutLabelSettings } from '@/lib/settings'
import { cn } from '@/lib/utils'

export default function SettingsPage() {
  const { data, isLoading, isError, error, refetch, isFetching } = useAllSettings()
  const [search, setSearch] = useState('')

  const filtered = useMemo(() => {
    if (!data) return []
    // Model-label keys are edited on the Models page only (I4).
    const services = withoutLabelSettings(data.services)
    if (!search.trim()) return services

    const q = search.toLowerCase()
    return services
      .map((svc) => ({
        ...svc,
        settings: svc.settings.filter(
          (s) =>
            s.key.toLowerCase().includes(q) ||
            s.category.toLowerCase().includes(q) ||
            (s.description?.toLowerCase().includes(q) ?? false),
        ),
      }))
      .filter(
        (svc) =>
          svc.settings.length > 0 ||
          svc.service_name.toLowerCase().includes(q) ||
          (svc.display_name?.toLowerCase().includes(q) ?? false),
      )
  }, [data, search])

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <RefreshCw className="animate-spin text-[var(--color-primary)]" size={24} />
      </div>
    )
  }

  if (isError) {
    return (
      <div className="py-20 text-center">
        <p className="mb-2 text-red-500">Failed to load settings</p>
        <p className="mb-4 text-sm text-[var(--color-text-muted)]">
          {errorMessage(error, 'Unknown error')}
        </p>
        <button
          onClick={() => refetch()}
          className="rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm text-white hover:opacity-90"
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-4xl space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold text-[var(--color-text)]">Settings</h1>

        <div className="flex items-center gap-2 text-xs text-[var(--color-text-muted)]">
          {data && (
            <span>
              {data.successful_services}/{data.total_services} modules
            </span>
          )}
          <button
            onClick={() => refetch()}
            disabled={isFetching}
            className={cn(
              'rounded-lg p-1.5 hover:bg-[var(--color-surface-alt)]',
              isFetching && 'animate-spin',
            )}
            title="Refresh"
          >
            <RefreshCw size={14} />
          </button>
        </div>
      </div>

      <div className="relative">
        <Search
          size={16}
          className="absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-text-muted)]"
        />
        <input
          type="text"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search settings..."
          className={cn(
            'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] py-2 pl-9 pr-3',
            'text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]',
            'outline-none focus:ring-2 focus:ring-[var(--color-primary)]',
          )}
        />
      </div>

      <p className="text-xs text-[var(--color-text-muted)]">
        Model choices (which model each label runs, GPUs, context, remote endpoints) are set on the{' '}
        <Link to="/models" className="text-[var(--color-primary)] hover:underline">
          Models
        </Link>{' '}
        page.
      </p>

      <div className="space-y-3">
        {filtered.map((svc, i) => (
          <ServiceCard key={svc.service_name} result={svc} defaultExpanded={i === 0} />
        ))}

        {filtered.length === 0 && (
          <p className="py-8 text-center text-sm text-[var(--color-text-muted)]">
            {search ? 'No settings match your search' : 'No services found'}
          </p>
        )}
      </div>
    </div>
  )
}

import DisplaySettings from '@/components/display/DisplaySettings'
import { useState, useMemo } from 'react'
import { Power, RefreshCw, Search, SearchX, SlidersHorizontal } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useAllSettings } from '@/hooks/useSettings'
import { Button, Card, EmptyState, ErrorState, Input, PageHeader, SkeletonList } from '@/components/ui'
import ServiceCard from '@/components/settings/ServiceCard'
import { errorMessage } from '@/lib/errors'
import { withoutLabelSettings } from '@/lib/settings'
import { restartJarvisd } from '@/lib/restart'
import { useFeatureAvailable } from '@/hooks/useFeature'
import { cn } from '@/lib/utils'

export default function SettingsPage() {
  const { data, isLoading, isError, error, refetch, isFetching } = useAllSettings()
  const [search, setSearch] = useState('')
  // AD8: hidden once POST /api/system/restart answers 404 (an older jarvisd).
  const canRestart = useFeatureAvailable('restart')
  const [restarting, setRestarting] = useState(false)

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

  const restart = () => {
    if (!window.confirm('Restart the server now? Voice and the admin are unavailable for a few seconds.')) return
    setRestarting(true)
    void restartJarvisd().finally(() => setRestarting(false))
  }

  let body
  if (isLoading) {
    body = (
      <Card flush>
        <SkeletonList rows={8} label="Loading settings" />
      </Card>
    )
  } else if (isError) {
    body = (
      <Card>
        <ErrorState title="Couldn't load settings" message={errorMessage(error)} onRetry={() => refetch()} />
      </Card>
    )
  } else if (filtered.length === 0) {
    body = (
      <Card>
        {search ? (
          <EmptyState
            icon={<SearchX size={20} />}
            title="No settings match"
            description="Search by a setting's key, its category or a word from its description."
            action={<Button onClick={() => setSearch('')}>Clear search</Button>}
          />
        ) : (
          <EmptyState icon={<SlidersHorizontal size={20} />} title="No modules answered" description="Settings appear here once the server's modules are running." />
        )}
      </Card>
    )
  } else {
    body = (
      <div className="space-y-3">
        {filtered.map((svc, i) => (
          <ServiceCard key={svc.service_name} result={svc} defaultExpanded={i === 0} />
        ))}
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title="Settings"
        description={
          <>
            How each part of Jarvis Automation behaves. Model choices are on{' '}
            <Link to="/admin/models" className="text-primary underline-offset-2 hover:underline">
              Models
            </Link>
            .
          </>
        }
        actions={
          <>
            {canRestart && (
              <Button
                icon={<Power size={16} aria-hidden />}
                loading={restarting}
                onClick={restart}
                title="Settings that need a restart apply after this"
              >
                Restart server
              </Button>
            )}
            <Button
              variant="ghost"
              size="icon"
              aria-label="Refresh"
              title="Refresh"
              disabled={isFetching}
              onClick={() => refetch()}
            >
              <RefreshCw size={16} aria-hidden className={cn(isFetching && 'motion-safe:animate-spin')} />
            </Button>
          </>
        }
      />

      <div className="mb-4 flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-4">
        <div className="relative min-w-0 flex-1">
          <Search size={16} aria-hidden className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-text-muted" />
          <Input
            type="search"
            aria-label="Search settings"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search settings…"
            className="pl-9"
          />
        </div>
        {data && (
          <p className="text-small text-text-muted tabular-nums">
            {data.successful_services} of {data.total_services} modules loaded
          </p>
        )}
      </div>

      <DisplaySettings scope="defaults" label="Display defaults" />
      {body}
    </div>
  )
}

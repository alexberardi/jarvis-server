import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Globe, Route } from 'lucide-react'
import { toast } from 'sonner'
import { getServiceSettings, updateSetting } from '@/api/settings'
import { useLabels } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { Banner, ErrorState, Skeleton } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import { cn } from '@/lib/utils'
import type { AggregatedSettingsResponse } from '@/types/settings'
import { FEATURES, SLOT_TITLE, cloudProvider, type Feature, type Slot } from './features'
import ModelCard from './ModelCard'

function settingValue(data: AggregatedSettingsResponse | undefined, key: string): unknown {
  for (const svc of data?.services ?? []) {
    const s = svc.settings.find((x) => x.key === key)
    if (s) return s.value
  }
  return undefined
}

function FeatureRow({ feature, provider }: { feature: Feature; provider: string }) {
  const qc = useQueryClient()
  const queryKey = ['settings', feature.service]
  const settings = useQuery({ queryKey, queryFn: () => getServiceSettings(feature.service) })
  const set = useMutation<unknown, Error, Slot>({
    mutationFn: (value) => updateSetting(feature.service, feature.key, value),
    onSuccess: (_, value) => {
      qc.setQueryData<AggregatedSettingsResponse>(queryKey, (old) =>
        old && {
          ...old,
          services: old.services.map((svc) => ({
            ...svc,
            settings: svc.settings.map((s) => (s.key === feature.key ? { ...s, value } : s)),
          })),
        },
      )
      void qc.invalidateQueries({ queryKey: ['settings'] })
      toast.success(`${feature.title}: ${SLOT_TITLE[value]}`)
    },
    onError: (err) => toast.error(errorMessage(err, `Could not set ${feature.title.toLowerCase()}'s model`)),
  })
  const id = `feature-${feature.key.replace(/\W/g, '-')}`
  if (settings.isLoading) return <Skeleton className="h-10 w-full" />
  if (settings.isError) {
    return (
      <ErrorState
        className="py-4"
        title={`Couldn't load ${feature.title.toLowerCase()}'s model`}
        message={errorMessage(settings.error, 'The server did not answer.')}
        onRetry={() => settings.refetch()}
      />
    )
  }
  const saved = (settingValue(settings.data, feature.key) as Slot | undefined) ?? 'auto'
  const value = set.isPending && set.variables ? set.variables : saved

  return (
    <div className="space-y-3 py-4 first:pt-0 last:pb-0" data-testid={id}>
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor={id} className="text-small font-medium text-text sm:w-40">
          {feature.title}
        </label>
        <select
          id={id}
          value={value}
          disabled={set.isPending}
          onChange={(e) => set.mutate(e.target.value as Slot)}
          className={cn(fieldControl, 'w-full sm:ml-auto sm:w-auto sm:max-w-xs')}
        >
          <option value="auto">Auto ({feature.auto})</option>
          <option value="live">Live</option>
          <option value="background">Background</option>
          <option value="cloud" disabled={!provider && value !== 'cloud'}>
            {provider ? `Cloud (${provider})` : 'Cloud (not set up)'}
          </option>
        </select>
      </div>
      {value === 'cloud' &&
        (provider ? (
          <Banner tone="warning" icon={<Globe size={16} />}>
            {feature.notice(provider)}
          </Banner>
        ) : (
          <Banner tone="danger" icon={<Globe size={16} />}>
            The cloud slot isn't set up, so {feature.title.toLowerCase()} fails until it is. Set it up under Labels, or
            pick another model.
          </Banner>
        ))}
    </div>
  )
}

/**
 * FeatureModelsCard picks the model slot each feature runs on: auto (the feature's own local
 * pick), live, background or the optional cloud slot, and says what goes to the cloud provider.
 */
export default function FeatureModelsCard() {
  const labels = useLabels()
  const cloud = labels.data?.labels.find((l) => l.label === 'cloud')?.config
  const provider = cloudProvider(cloud)

  return (
    <ModelCard
      title="Feature models"
      icon={Route}
      description="Which model each feature runs on. Auto stays on this machine; only a feature you set to cloud uses the cloud slot."
    >
      <div className="divide-y divide-border">
        {FEATURES.map((f) => (
          <FeatureRow key={f.key} feature={f} provider={provider} />
        ))}
      </div>
    </ModelCard>
  )
}

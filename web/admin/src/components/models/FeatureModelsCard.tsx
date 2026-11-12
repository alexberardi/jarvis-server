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
import {
  FEATURE_GROUPS,
  SLOT_TITLE,
  cloudProvider,
  slotSeesImages,
  type Feature,
  type Slot,
} from './features'
import ModelCard from './ModelCard'

function settingValue(data: AggregatedSettingsResponse | undefined, key: string): unknown {
  for (const svc of data?.services ?? []) {
    const s = svc.settings.find((x) => x.key === key)
    if (s) return s.value
  }
  return undefined
}

/** Why a chosen slot cannot run the feature, and what to do about it. */
function slotBlockedMessage(feature: Feature, slot: Exclude<Slot, 'auto'>, blocked: string): string {
  const name = feature.title.toLowerCase()
  if (slot === 'cloud' && blocked === 'not set up') {
    return `The cloud slot isn't set up, so ${name} fails until it is. Set it up under Labels, or pick another model.`
  }
  if (blocked === 'no vision') {
    return `${feature.title} sends images, and the ${slot} model can't see them, so it fails until that model gets a vision projector. Add one under Labels, or pick another model.`
  }
  return `The ${slot} slot can't run ${name} right now (${blocked}).`
}

/** The slots a feature may run on: everything the slot picker offers, with why not when it can't. */
type SlotState = { blocked?: string }

function FeatureRow({
  feature,
  provider,
  slots,
}: {
  feature: Feature
  provider: string
  slots: Record<Exclude<Slot, 'auto'>, SlotState>
}) {
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
          <option value="auto">Auto — {SLOT_TITLE[feature.auto].toLowerCase()}</option>
          {(['live', 'background', 'cloud'] as const).map((slot) => {
            const blocked = slots[slot].blocked
            const name = slot === 'cloud' && provider ? `Cloud (${provider})` : SLOT_TITLE[slot]
            return (
              // A slot already chosen stays selectable, so a choice made before the model
              // changed is visible and can be moved rather than silently re-reading as another.
              <option key={slot} value={slot} disabled={Boolean(blocked) && value !== slot}>
                {blocked ? `${name} — ${blocked}` : name}
              </option>
            )
          })}
        </select>
      </div>
      {value === 'cloud' && provider && (
        <Banner tone="warning" icon={<Globe size={16} />}>
          {feature.notice(provider)}
        </Banner>
      )}
      {value !== 'auto' && slots[value].blocked && (
        <Banner tone="danger" icon={<Globe size={16} />}>
          {slotBlockedMessage(feature, value, slots[value].blocked ?? '')}
        </Banner>
      )}
    </div>
  )
}

/**
 * FeatureModelsCard picks the model slot each feature runs on: auto (the feature's own local
 * pick), live, background or the optional cloud slot, and says what goes to the cloud provider.
 */
export default function FeatureModelsCard() {
  const labels = useLabels()
  const config = (label: string) => labels.data?.labels.find((l) => l.label === label)?.config
  const cloud = config('cloud')
  const provider = cloudProvider(cloud)

  const slotStates = (feature: Feature): Record<Exclude<Slot, 'auto'>, SlotState> => {
    const state = (slot: Exclude<Slot, 'auto'>): SlotState => {
      if (slot === 'cloud' && !provider) return { blocked: 'not set up' }
      // Vision is only known once the labels load; until then nothing is blocked, so a slow
      // request never makes a valid choice look broken.
      if (feature.vision && labels.data && !slotSeesImages(config(slot))) return { blocked: 'no vision' }
      return {}
    }
    return { live: state('live'), background: state('background'), cloud: state('cloud') }
  }

  return (
    <ModelCard
      title="Feature models"
      icon={Route}
      description="Which model each feature runs on. Auto stays on this machine; only a feature you set to cloud uses the cloud slot."
    >
      <div className="space-y-6">
        {FEATURE_GROUPS.map((group) => (
          <section key={group.id} aria-labelledby={`feature-group-${group.id}`}>
            <h3 id={`feature-group-${group.id}`} className="text-small font-semibold text-text">
              {group.title}
            </h3>
            <p className="mt-1 text-caption text-text-muted">{group.blurb}</p>
            <div className="mt-3 divide-y divide-border border-t border-border">
              {group.features.map((f) => (
                <FeatureRow key={f.key} feature={f} provider={provider} slots={slotStates(f)} />
              ))}
            </div>
          </section>
        ))}
      </div>
    </ModelCard>
  )
}

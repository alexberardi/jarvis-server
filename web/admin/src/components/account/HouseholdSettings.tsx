import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getHouseholdSettings, putHouseholdSetting } from '@/api/household'
import { Alert, Card, CardHeader, Switch, toast } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'

/** The household switches the web shows (cc's allowlist has more; the app edits those). */
const SWITCHES = [
  {
    key: 'watches.enabled',
    label: 'Watches',
    help: 'Lets members ask Jarvis to watch a web page, a search or a signal and tell them when it changes.',
  },
  {
    key: 'errands.autonomous_enabled',
    label: 'Errand autonomy',
    help: "Lets the household's rules run background errands without asking first.",
  },
] as const

const ADMIN_ROLES = new Set(['admin', 'owner'])

/**
 * HouseholdSettings is the active household's feature switches on the Account page: any member
 * sees them, only a household admin or owner can flip them (cc enforces the same).
 */
export default function HouseholdSettings() {
  const { households, activeHouseholdId } = useAuth()
  const qc = useQueryClient()
  const hh = activeHouseholdId
  const membership = households.find((h) => h.id === hh)
  const canEdit = !!membership && ADMIN_ROLES.has(membership.role)
  const queryKey = ['household-settings', hh]
  const { data, isLoading, error } = useQuery({
    queryKey,
    queryFn: () => getHouseholdSettings(hh),
    enabled: !!hh,
  })
  const write = useMutation({
    mutationFn: ({ key, value }: { key: string; value: boolean }) => putHouseholdSetting(hh, key, value),
    onSuccess: (_, { key, value }) => {
      qc.setQueryData<Record<string, unknown>>(queryKey, (old) => ({ ...old, [key]: value }))
      toast.success('Saved')
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not save the setting')),
  })

  if (!hh) return null
  return (
    <Card as="section" aria-labelledby="household-settings">
      <CardHeader
        id="household-settings"
        title={`Household${membership ? `: ${membership.name}` : ''}`}
        description={membership && !canEdit ? 'Only a household admin can change these.' : undefined}
      />
      {error && (
        <Alert tone="danger" title="Couldn't load the household's settings" className="mb-4">
          {errorMessage(error, 'Reload the page to try again.')}
        </Alert>
      )}
      <ul className="space-y-4">
        {SWITCHES.map((s) => (
          <li key={s.key}>
            <Switch
              id={`hh-${s.key.replace(/\W/g, '-')}`}
              label={s.label}
              description={`${s.help} Off by default.`}
              checked={data?.[s.key] === true}
              disabled={!canEdit || isLoading || !!error || write.isPending}
              onCheckedChange={(value) => write.mutate({ key: s.key, value })}
            />
          </li>
        ))}
      </ul>
    </Card>
  )
}

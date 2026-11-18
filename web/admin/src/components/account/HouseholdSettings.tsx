import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getHouseholdSettings, putHouseholdSetting } from '@/api/household'
import { Alert, Card, CardHeader, Switch, toast } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'

/**
 * The household switches (mobile's Household settings), with each one's default when the
 * household never set it. Learning from conversations needs memory on, as on mobile.
 */
const SWITCHES = [
  {
    key: 'web_search.enabled',
    label: 'Web search',
    help: 'Lets Jarvis search the internet for current events, prices and news. Off, it answers only from what it knows and nothing leaves your network.',
    on: false,
  },
  {
    key: 'pantry.enabled',
    label: 'Pantry',
    help: 'Browse and install packages from the Pantry. On, this app, your nodes and your server contact the Pantry, which sees your IP address and what you browse and install.',
    on: false,
  },
  {
    key: 'watches.enabled',
    label: 'Watches',
    help: 'Lets members ask Jarvis to watch a web page, a search or a signal and tell them when it changes.',
    on: false,
  },
  {
    key: 'errands.autonomous_enabled',
    label: 'Errand autonomy',
    help: "Lets the household's rules run background errands without asking first.",
    on: false,
  },
  {
    key: 'memory.enabled',
    label: 'Memory',
    help: 'Lets Jarvis remember what you ask it to, recall it later and forget it on request. Off, it neither stores nor uses memories here.',
    on: true,
  },
  {
    key: 'memory.extraction_enabled',
    label: 'Learn from conversations',
    help: 'Lets Jarvis pick up useful facts from what you say, not only what you ask it to remember. Only for speakers it recognises.',
    on: true,
    needs: 'memory.enabled',
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
        title="Features"
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
              description={`${s.help} ${s.on ? 'On' : 'Off'} by default.`}
              checked={(data?.[s.key] ?? s.on) === true}
              disabled={!canEdit || isLoading || !!error || write.isPending || ('needs' in s && data?.[s.needs] === false)}
              onCheckedChange={(value) => write.mutate({ key: s.key, value })}
            />
          </li>
        ))}
      </ul>
    </Card>
  )
}

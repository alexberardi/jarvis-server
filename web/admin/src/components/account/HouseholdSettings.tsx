import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { getHouseholdSettings, putHouseholdSetting } from '@/api/household'
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
    <section
      aria-labelledby="household-settings"
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-6"
    >
      <h2 id="household-settings" className="text-lg font-medium text-[var(--color-text)]">
        Household{membership ? `: ${membership.name}` : ''}
      </h2>
      {membership && !canEdit && (
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">Only a household admin can change these.</p>
      )}
      {error && (
        <p role="alert" className="mt-3 rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-500">
          {errorMessage(error, 'Could not load the household settings')}
        </p>
      )}
      <ul className="mt-3 space-y-3">
        {SWITCHES.map((s) => {
          const id = `hh-${s.key.replace(/\W/g, '-')}`
          return (
            <li key={s.key} className="flex items-start gap-3">
              <input
                id={id}
                type="checkbox"
                role="switch"
                aria-describedby={`${id}-help`}
                checked={data?.[s.key] === true}
                disabled={!canEdit || isLoading || !!error || write.isPending}
                onChange={(e) => write.mutate({ key: s.key, value: e.target.checked })}
                className="mt-1 h-4 w-4 shrink-0 accent-[var(--color-primary)]"
              />
              <div>
                <label htmlFor={id} className="text-sm font-medium text-[var(--color-text)]">
                  {s.label}
                </label>
                <p id={`${id}-help`} className="text-sm text-[var(--color-text-muted)]">
                  {s.help} Off by default.
                </p>
              </div>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

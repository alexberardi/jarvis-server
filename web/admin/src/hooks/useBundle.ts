import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { getHouseholdSettings, putHouseholdSetting } from '@/api/household'
import { BUNDLE_KEY } from '@/api/store'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { parseBundle, withBundle } from '@/lib/store'

const ADMIN_ROLES = new Set(['admin', 'owner'])

/**
 * useBundle is the active household's default bundle (pantry.default_bundle): its package names,
 * whether the caller may change it (a household admin), and a toggle for one package.
 */
export function useBundle() {
  const { households, activeHouseholdId: hh } = useAuth()
  const qc = useQueryClient()
  const household = households.find((h) => h.id === hh)
  const settingsKey = ['household-settings', hh]
  const settings = useQuery({ queryKey: settingsKey, queryFn: () => getHouseholdSettings(hh), enabled: !!hh })
  const bundle = parseBundle(settings.data?.[BUNDLE_KEY])
  const write = useMutation({
    mutationFn: (value: string) => putHouseholdSetting(hh, BUNDLE_KEY, value),
    onSuccess: (_, value) => qc.setQueryData<Record<string, unknown>>(settingsKey, (old) => ({ ...old, [BUNDLE_KEY]: value })),
    onError: (err) => toast.error(errorMessage(err, 'Could not change the bundle')),
  })
  return {
    bundle,
    canEdit: !!household && ADMIN_ROLES.has(household.role),
    toggle: (name: string, on: boolean) => write.mutate(withBundle(bundle, name, on)),
  }
}

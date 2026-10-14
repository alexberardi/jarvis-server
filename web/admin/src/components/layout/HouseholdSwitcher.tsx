import { toast } from 'sonner'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

/**
 * HouseholdSwitcher shows only with a choice to make: two or more households (webapp 01 §4.4).
 * Chat, its node picker, Add-ons and the inbox follow the active household; the Admin pages
 * are install-wide and ignore it. The header carries it from sm up, the nav drawer on phones.
 */
export default function HouseholdSwitcher({ className }: { className?: string }) {
  const { households, activeHouseholdId, switchHousehold } = useAuth()
  if (households.length < 2) return null

  async function pick(id: string) {
    try {
      await switchHousehold(id)
    } catch (err) {
      toast.error(errorMessage(err, 'Could not switch household'))
    }
  }

  return (
    <select
      aria-label="Household"
      value={activeHouseholdId}
      onChange={(e) => void pick(e.target.value)}
      className={cn(
        'h-9 min-w-0 truncate rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-2 text-sm text-[var(--color-text)] sm:max-w-[12rem]',
        className,
      )}
    >
      {!households.some((h) => h.id === activeHouseholdId) && <option value={activeHouseholdId}>Household</option>}
      {households.map((h) => (
        <option key={h.id} value={h.id}>
          {h.name}
        </option>
      ))}
    </select>
  )
}

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  getHouseholdPrompt,
  importPrompt,
  previewPrompt,
  resetPrompt,
  savePrompt,
  setPromptBase,
  type HouseholdPrompt,
  type PromptPrefs,
  type PromptPreview,
} from '@/api/prompts'
import { useAuth } from './useAuth'

export const promptKey = (householdId: string) => ['household-prompt', householdId] as const

/** useHouseholdPrompt reads the active household's voice prompt. */
export function useHouseholdPrompt() {
  const { activeHouseholdId } = useAuth()
  return useQuery<HouseholdPrompt>({
    queryKey: promptKey(activeHouseholdId),
    queryFn: () => getHouseholdPrompt(activeHouseholdId),
    enabled: !!activeHouseholdId,
  })
}

/** A mutation on the active household's prompt: its answer is the new prompt, so it is stored as is. */
function usePromptMutation<V>(fn: (householdId: string, v: V) => Promise<HouseholdPrompt>) {
  const { activeHouseholdId } = useAuth()
  const qc = useQueryClient()
  return useMutation<HouseholdPrompt, Error, V>({
    mutationFn: (v) => fn(activeHouseholdId, v),
    onSuccess: (p) => {
      qc.setQueryData(promptKey(activeHouseholdId), p)
      void qc.invalidateQueries({ queryKey: promptKey(activeHouseholdId) })
    },
  })
}

export function useSavePrompt() {
  return usePromptMutation<{ text: string } & Partial<PromptPrefs>>(savePrompt)
}

export function useResetPrompt() {
  return usePromptMutation<void>((hh) => resetPrompt(hh))
}

export function useSetPromptBase() {
  return usePromptMutation<string>(setPromptBase)
}

export function useImportPrompt() {
  return usePromptMutation<{ name: string; text: string; prefs?: Partial<PromptPrefs> }>((hh, v) =>
    importPrompt(hh, v.name, v.text, v.prefs),
  )
}

/** usePreviewPrompt renders a draft; it changes nothing, so nothing is invalidated. */
export function usePreviewPrompt() {
  const { activeHouseholdId } = useAuth()
  return useMutation<PromptPreview, Error, { text: string; nodeId: string }>({
    mutationFn: ({ text, nodeId }) => previewPrompt(activeHouseholdId, text, nodeId),
  })
}

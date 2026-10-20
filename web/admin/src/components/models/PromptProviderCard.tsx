import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, MessageSquareCode } from 'lucide-react'
import { toast } from 'sonner'
import type { ModeSetting } from '@/api/llm'
import { updateSetting } from '@/api/settings'
import { mmKeys, usePromptProvider, useSetPromptProvider } from '@/hooks/useModelManager'
import { useSetupState } from '@/hooks/useSetup'
import { errorMessage } from '@/lib/errors'
import { Badge, Button, ErrorState, Skeleton } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import { cn } from '@/lib/utils'
import ModelCard from './ModelCard'

const SOURCE_TEXT: Record<string, string> = {
  model: 'from model',
  setting: 'set by you',
}

/** Where a mode's detected value came from; null when the server doesn't say (it was overridden). */
function detectedText(m: ModeSetting): string | null {
  if (!m.detected) return 'not detected'
  if (m.source === 'template') return "from the model's chat template"
  if (m.source === 'remote') return 'remote model'
  return null
}

/**
 * ModeRow shows one LLM mode setting (docs/cc/prompt-contract.md): what turns use, whether it was
 * detected or set by the superuser, and a select to override it ("auto" follows the detection).
 */
function ModeRow({ id, label, mode, options }: { id: string; label: string; mode: ModeSetting; options: string[] }) {
  const qc = useQueryClient()
  const set = useMutation<unknown, Error, string>({
    mutationFn: (value) => updateSetting('cc', `llm.${id}`, value),
    onSuccess: (_, value) => {
      toast.success(`${label} set to ${value}`)
      void qc.invalidateQueries({ queryKey: mmKeys.prompt })
    },
    onError: (err) => toast.error(errorMessage(err, `Could not set ${label.toLowerCase()}`)),
  })
  const detected = detectedText(mode)
  const setByYou = mode.value !== 'auto'

  return (
    <div className="flex flex-wrap items-center gap-2 py-3 first:pt-0 last:pb-0">
      <label htmlFor={`llm-${id}`} className="w-32 text-small font-medium text-text">
        {label}
      </label>
      <code className="font-mono text-small text-text">{mode.effective}</code>
      <Badge>{detected ?? `detected: ${mode.detected}`}</Badge>
      {setByYou ? (
        <Badge tone="primary">set by you</Badge>
      ) : (
        <span className="text-small text-text-muted">households' prompts may set their own</span>
      )}
      <select
        id={`llm-${id}`}
        value={mode.value}
        disabled={set.isPending}
        onChange={(e) => set.mutate(e.target.value)}
        className={cn(fieldControl, 'w-full sm:ml-auto sm:w-auto sm:max-w-xs')}
      >
        <option value="auto">auto ({mode.detected || 'not detected'})</option>
        {options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    </div>
  )
}

/**
 * PromptProviderCard shows how voice prompts are shaped for the live model (AD4): the effective
 * provider and where it comes from, an override, and a pick-list when the model names none.
 */
export default function PromptProviderCard() {
  const { data, isLoading, isError, error, refetch } = usePromptProvider()
  const set = useSetPromptProvider()
  const setup = useSetupState()
  const [editing, setEditing] = useState(false)
  const [choice, setChoice] = useState('')

  function save(value: string) {
    set.mutate(value, {
      onSuccess: (p) => {
        toast.success(value ? `Prompt provider set to ${p.effective}` : 'Prompt provider follows the live model again')
        setEditing(false)
      },
      onError: (err) => toast.error(errorMessage(err, 'Could not set the prompt provider')),
    })
  }

  const options = data?.options ?? []
  // A10 F18: with no live model assigned yet, "names none" is expected (installing one
  // supplies it), so there is nothing to warn about or pick until one is. Unknown = assigned.
  // While that is still loading, wait rather than flash the warning.
  const awaitingModel =
    (setup.data?.superuser === true && setup.data.models_configured === false) || (setup.isPending && !setup.isError)
  const unset = Boolean(data && !data.derived && !data.value)
  // AD4: the pick-list is offered when the model declares no provider; otherwise "override".
  const mustPick = unset && !awaitingModel
  const warn = Boolean(data && !data.valid && !(awaitingModel && unset))

  return (
    <ModelCard
      title="Prompt and model behaviour"
      icon={MessageSquareCode}
      description="The default prompt for households without their own, and how the live model calls tools and thinks."
    >
      {isLoading && (
        <div role="status" aria-label="Loading the prompt provider" className="space-y-2">
          <Skeleton className="w-1/3" />
          <Skeleton className="w-2/3" />
        </div>
      )}
      {isError && (
        <ErrorState
          className="py-6"
          title="Couldn't read the prompt provider"
          message={errorMessage(error, 'The server did not answer.')}
          onRetry={() => refetch()}
        />
      )}
      {data && (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <code className="font-mono text-body text-text">{data.effective || '(none)'}</code>
            {data.source && <Badge tone={data.source === 'setting' ? 'primary' : 'neutral'}>{SOURCE_TEXT[data.source] ?? data.source}</Badge>}
            {data.source === 'setting' && data.derived && data.derived !== data.value && (
              <span className="text-small text-text-muted">(the model says {data.derived})</span>
            )}
          </div>

          {awaitingModel && unset && (
            <p className="text-small text-text-muted">
              It comes from the live model once one is assigned; catalog models name theirs.
            </p>
          )}

          {warn && (
            <p role="alert" className="flex items-start gap-2 rounded-control bg-danger-soft px-3 py-2 text-small text-text">
              <AlertTriangle size={16} aria-hidden className="mt-0.5 shrink-0 text-danger" />
              {data.effective
                ? `"${data.effective}" is not a known prompt provider, so voice turns will fail. Pick one below.`
                : 'No prompt provider: the live model names none, so voice turns will fail until you pick one.'}
            </p>
          )}

          {(editing || mustPick || warn) && (
            <div className="flex flex-wrap items-center gap-2">
              <label htmlFor="prompt-provider" className="sr-only">
                Prompt provider
              </label>
              {options.length > 0 ? (
                <select
                  id="prompt-provider"
                  value={choice || data.value || ''}
                  onChange={(e) => setChoice(e.target.value)}
                  className={cn(fieldControl, 'max-w-xs')}
                >
                  <option value="" disabled>
                    Choose…
                  </option>
                  {options.map((o) => (
                    <option key={o} value={o}>
                      {o}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  id="prompt-provider"
                  value={choice}
                  onChange={(e) => setChoice(e.target.value)}
                  className={cn(fieldControl, 'max-w-xs')}
                />
              )}
              <Button
                variant="primary"
                disabled={!(choice || data.value)}
                loading={set.isPending}
                onClick={() => save(choice || data.value)}
              >
                Use this
              </Button>
              {editing && <Button onClick={() => setEditing(false)}>Cancel</Button>}
            </div>
          )}

          <div className="flex flex-wrap gap-2">
            {!editing && !mustPick && (data.valid || (awaitingModel && unset)) && (
              <Button size="sm" onClick={() => setEditing(true)}>
                Override
              </Button>
            )}
            {data.source === 'setting' && (
              <Button size="sm" disabled={set.isPending} onClick={() => save('')}>
                {data.derived ? `Use the model's (${data.derived})` : 'Clear override'}
              </Button>
            )}
          </div>

          <div className="divide-y divide-border border-t border-border pt-3">
            <ModeRow id="tool_calling" label="Tool calling" mode={data.tool_calling} options={['native', 'text']} />
            <ModeRow id="thinking_switch" label="Thinking switch" mode={data.thinking_switch} options={['suffix', 'none']} />
          </div>
        </div>
      )}
    </ModelCard>
  )
}

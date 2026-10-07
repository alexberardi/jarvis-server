import { useState } from 'react'
import { AlertTriangle, MessageSquareCode } from 'lucide-react'
import { toast } from 'sonner'
import { usePromptProvider, useSetPromptProvider } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { buttonClass, inputClass } from './styles'
import { Pill, Section } from './ui'

const SOURCE_TEXT: Record<string, string> = {
  model: 'from model',
  setting: 'set by you',
}

/**
 * PromptProviderCard shows how voice prompts are shaped for the live model (AD4): the effective
 * provider and where it comes from, an override, and a pick-list when the model names none.
 */
export default function PromptProviderCard() {
  const { data, isLoading, isError, error } = usePromptProvider()
  const set = useSetPromptProvider()
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
  // AD4: the pick-list is offered when the model declares no provider; otherwise "override".
  const mustPick = Boolean(data && !data.derived && !data.value)

  return (
    <Section
      title="Prompt provider"
      icon={MessageSquareCode}
      description="How voice turns are formatted for the live model. It normally comes from the model."
    >
      {isLoading && <p className="text-sm text-[var(--color-text-muted)]">Loading…</p>}
      {isError && <p className="text-sm text-red-500">{errorMessage(error, 'Could not read the prompt provider')}</p>}
      {data && (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <code className="text-sm text-[var(--color-text)]">{data.effective || '(none)'}</code>
            {data.source && <Pill tone={data.source === 'setting' ? 'info' : 'muted'}>{SOURCE_TEXT[data.source] ?? data.source}</Pill>}
            {data.source === 'setting' && data.derived && data.derived !== data.value && (
              <span className="text-xs text-[var(--color-text-muted)]">(the model says {data.derived})</span>
            )}
          </div>

          {!data.valid && (
            <p role="alert" className="flex items-start gap-2 rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-500">
              <AlertTriangle size={14} className="mt-0.5 shrink-0" />
              {data.effective
                ? `"${data.effective}" is not a known prompt provider, so voice turns will fail. Pick one below.`
                : 'No prompt provider: the live model names none, so voice turns will fail until you pick one.'}
            </p>
          )}

          {(editing || mustPick || !data.valid) && (
            <div className="flex flex-wrap items-center gap-2">
              <label htmlFor="prompt-provider" className="sr-only">
                Prompt provider
              </label>
              {options.length > 0 ? (
                <select
                  id="prompt-provider"
                  value={choice || data.value || ''}
                  onChange={(e) => setChoice(e.target.value)}
                  className={`${inputClass} max-w-xs`}
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
                  className={`${inputClass} max-w-xs`}
                />
              )}
              <button
                type="button"
                className={buttonClass.primary}
                disabled={set.isPending || !(choice || data.value)}
                onClick={() => save(choice || data.value)}
              >
                Use this
              </button>
              {editing && (
                <button type="button" className={buttonClass.secondary} onClick={() => setEditing(false)}>
                  Cancel
                </button>
              )}
            </div>
          )}

          <div className="flex flex-wrap gap-2">
            {!editing && !mustPick && data.valid && (
              <button type="button" className={buttonClass.secondary} onClick={() => setEditing(true)}>
                Override
              </button>
            )}
            {data.source === 'setting' && (
              <button type="button" className={buttonClass.secondary} disabled={set.isPending} onClick={() => save('')}>
                {data.derived ? `Use the model's (${data.derived})` : 'Clear override'}
              </button>
            )}
          </div>
        </div>
      )}
    </Section>
  )
}

import { useState } from 'react'
import { KeyRound } from 'lucide-react'
import { toast } from 'sonner'
import { useHfTokenSet, useSetHfToken } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { buttonClass, inputClass } from './styles'

/**
 * HfTokenPrompt stores `llm.hf_token` (a write-only secret) for gated repos. The Models page
 * shows it when a browse or install hits a 403; it is also always reachable from the HF section.
 */
export default function HfTokenPrompt({
  reason,
  onSaved,
  compact,
}: {
  reason?: string
  onSaved?: () => void
  compact?: boolean
}) {
  const { data: isSet } = useHfTokenSet()
  const save = useSetHfToken()
  const [token, setToken] = useState('')

  function submit() {
    const t = token.trim()
    if (!t) return
    save.mutate(t, {
      onSuccess: () => {
        setToken('')
        toast.success('Hugging Face token saved')
        onSaved?.()
      },
      onError: (err) => toast.error(`Could not save the token: ${errorMessage(err)}`),
    })
  }

  return (
    <div className={compact ? 'space-y-2' : 'space-y-2 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3'}>
      <div className="flex items-center gap-2 text-sm font-medium text-[var(--color-text)]">
        <KeyRound size={14} className="text-amber-500" />
        Hugging Face token
        <span className="text-xs font-normal text-[var(--color-text-muted)]">
          {isSet ? '(set; enter a new one to replace it)' : '(not set)'}
        </span>
      </div>
      {reason && <p className="text-xs text-[var(--color-text-muted)]">{reason}</p>}
      <p className="text-xs text-[var(--color-text-muted)]">
        Only needed for gated or private repos. Create a read token at huggingface.co → Settings → Access
        Tokens, and accept the model's licence on its page first. It is stored on this server and never
        shown again.
      </p>
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <label htmlFor="hf-token" className="sr-only">
          Hugging Face token
        </label>
        <input
          id="hf-token"
          type="password"
          autoComplete="off"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="hf_…"
          className={inputClass}
        />
        <button type="submit" disabled={!token.trim() || save.isPending} className={buttonClass.primary}>
          {save.isPending ? 'Saving…' : 'Save'}
        </button>
        {isSet && (
          <button
            type="button"
            className={buttonClass.secondary}
            disabled={save.isPending}
            onClick={() =>
              save.mutate(null, {
                onSuccess: () => toast.success('Hugging Face token removed'),
                onError: (err) => toast.error(errorMessage(err)),
              })
            }
          >
            Remove
          </button>
        )}
      </form>
    </div>
  )
}

import { useState } from 'react'
import { KeyRound } from 'lucide-react'
import { toast } from 'sonner'
import { useHfTokenSet, useSetHfToken } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { Banner, Button, Input } from '@/components/ui'

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

  const state = (
    <span className="text-small font-normal text-text-muted">
      {isSet ? '(set; enter a new one to replace it)' : '(not set)'}
    </span>
  )
  const body = (
    <div className="space-y-2">
      {reason && <p className="text-small text-text">{reason}</p>}
      <p className="text-small text-text-muted">
        Only needed for gated or private repos. Create a read token at huggingface.co → Settings → Access
        Tokens, and accept the model's licence on its page first. It is stored on this server and never
        shown again.
      </p>
      <form
        className="flex flex-wrap gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <Input
          id="hf-token"
          aria-label="Hugging Face token"
          wrapperClassName="min-w-0 flex-1 basis-48"
          type="password"
          autoComplete="off"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="hf_…"
        />
        <Button type="submit" variant="primary" disabled={!token.trim()} loading={save.isPending}>
          Save
        </Button>
        {isSet && (
          <Button
            disabled={save.isPending}
            onClick={() =>
              save.mutate(null, {
                onSuccess: () => toast.success('Hugging Face token removed'),
                onError: (err) => toast.error(errorMessage(err)),
              })
            }
          >
            Remove
          </Button>
        )}
      </form>
    </div>
  )

  if (!compact) {
    return (
      <Banner tone="warning" icon={<KeyRound size={16} />} title={<>Hugging Face token {state}</>}>
        {body}
      </Banner>
    )
  }
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-subheading text-text">
        <KeyRound size={16} aria-hidden className="text-warning" />
        Hugging Face token
        {state}
      </div>
      {body}
    </div>
  )
}

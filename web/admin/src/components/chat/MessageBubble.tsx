import { useState } from 'react'
import { ImageIcon, Loader2 } from 'lucide-react'
import type { ChatAction } from '@/api/chat'
import type { ChatMessage } from '@/lib/chatState'
import { optionLabel } from '@/lib/chatState'
import { cn } from '@/lib/utils'
import Markdown from './Markdown'

interface Props {
  message: ChatMessage
  /** The user's name for their own bubbles (display name, §3.7). */
  userName: string
  /** Validation options answer by sending the option as the next message. */
  onAnswer?: (text: string) => void
  onAction?: (action: ChatAction) => Promise<void>
  /** Only the latest answer's options and buttons are live. */
  latest: boolean
  busy: boolean
}

const actionStyle = (t?: string) =>
  t === 'primary'
    ? 'bg-[var(--color-primary)] text-white hover:opacity-90'
    : t === 'destructive'
      ? 'bg-red-600 text-white hover:opacity-90'
      : 'border border-[var(--color-border)] text-[var(--color-text)] hover:bg-[var(--color-surface-alt)]'

export default function MessageBubble({ message: m, userName, onAnswer, onAction, latest, busy }: Props) {
  const [running, setRunning] = useState<string | null>(null)
  const user = m.role === 'user'

  async function press(a: ChatAction) {
    if (!onAction) return
    setRunning(a.button_action)
    try {
      await onAction(a)
    } finally {
      setRunning(null)
    }
  }

  return (
    <div className={cn('flex', user ? 'justify-end' : 'justify-start')} data-role={m.role}>
      <div className={cn('max-w-[88%] min-w-0 sm:max-w-[75%]')}>
        {user && <p className="mb-0.5 text-right text-xs text-[var(--color-text-muted)]">{userName}</p>}
        <div
          className={cn(
            'rounded-2xl px-3.5 py-2 text-sm',
            user
              ? 'rounded-br-md bg-[var(--color-primary)] text-white'
              : 'rounded-bl-md border border-[var(--color-border)] bg-[var(--color-surface)] text-[var(--color-text)]',
            m.error && 'border-red-500/40 text-red-600 dark:text-red-400',
          )}
        >
          {user ? (
            <>
              {m.images ? (
                <p className="mb-1 flex items-center gap-1 text-xs opacity-80">
                  <ImageIcon size={12} aria-hidden="true" />
                  {m.images === 1 ? '1 photo' : `${m.images} photos`}
                </p>
              ) : null}
              {m.text && <p className="whitespace-pre-wrap break-words">{m.text}</p>}
            </>
          ) : m.pending && !m.text ? (
            <p className="flex items-center gap-2 text-[var(--color-text-muted)]" role="status">
              <Loader2 size={14} className="animate-spin" aria-hidden="true" />
              {m.status || 'Thinking...'}
            </p>
          ) : (
            <Markdown>{m.text}</Markdown>
          )}
        </div>

        {!user && m.reasoning && (
          <details className="mt-1 text-xs text-[var(--color-text-muted)]">
            <summary className="cursor-pointer select-none">Thinking</summary>
            <p className="mt-1 whitespace-pre-wrap border-l-2 border-[var(--color-border)] pl-2">{m.reasoning}</p>
          </details>
        )}

        {!user && m.validation && m.validation.options.length > 0 && (
          <div className="mt-2 flex flex-wrap gap-2" aria-label="Answers">
            {m.validation.options.map((o, i) => {
              const label = optionLabel(o)
              return (
                <button
                  key={`${label}-${i}`}
                  type="button"
                  disabled={!latest || busy}
                  onClick={() => onAnswer?.(label)}
                  className="rounded-full border border-[var(--color-primary)] px-3 py-1 text-sm text-[var(--color-primary)] hover:bg-[var(--color-primary)]/10 disabled:opacity-50"
                >
                  {label}
                </button>
              )
            })}
          </div>
        )}

        {!user && m.actions && m.actions.length > 0 && (
          <div className="mt-2 flex flex-wrap items-center gap-2">
            {m.actionDone ? (
              <p className="text-sm text-[var(--color-text-muted)]" role="status">
                {m.actionDone}
              </p>
            ) : (
              m.actions.map((a) => (
                <button
                  key={a.button_action}
                  type="button"
                  disabled={running !== null}
                  onClick={() => void press(a)}
                  className={cn(
                    'inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium disabled:opacity-50',
                    actionStyle(a.button_type),
                  )}
                >
                  {running === a.button_action && <Loader2 size={14} className="animate-spin" aria-hidden="true" />}
                  {a.button_text}
                </button>
              ))
            )}
          </div>
        )}
      </div>
    </div>
  )
}

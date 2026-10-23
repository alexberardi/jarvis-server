import { type CSSProperties, useState } from 'react'
import { ImageIcon } from 'lucide-react'
import type { ChatAction } from '@/api/chat'
import type { ChatMessage } from '@/lib/chatState'
import { optionLabel } from '@/lib/chatState'
import { cn } from '@/lib/utils'
import { Button, type ButtonVariant } from '@/components/ui'
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

const actionVariant = (t?: string): ButtonVariant => (t === 'primary' ? 'primary' : t === 'destructive' ? 'danger' : 'secondary')

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
    <div className={cn('flex', user ? 'justify-end' : 'justify-start')}>
      <div className="max-w-[88%] min-w-0 sm:max-w-[75%]">
        <p className={cn('mb-1 text-caption text-text-muted', user && 'text-right')}>{user ? userName : 'Jarvis'}</p>
        {/* The name sits outside data-role, which holds only what was said (and its buttons). */}
        <div data-role={m.role}>
          <div
            className={cn(
              'rounded-bubble px-4 py-2.5 text-body',
              user ? 'rounded-br-control border border-primary/25 bg-primary-soft text-text' : 'rounded-bl-control border border-border bg-surface-alt text-text',
              m.error && 'border-danger/40 bg-danger-soft text-danger',
            )}
          >
            {user ? (
              <>
                {m.images ? (
                  <p className="mb-1 flex items-center gap-1 text-caption">
                    <ImageIcon size={12} aria-hidden="true" />
                    {m.images === 1 ? '1 photo' : `${m.images} photos`}
                  </p>
                ) : null}
                {m.text && <p className="whitespace-pre-wrap break-words">{m.text}</p>}
              </>
            ) : m.pending && !m.text ? (
              <p className="flex items-center gap-2.5 text-text-muted" role="status">
                <span className="flex items-center gap-[3px]" aria-hidden="true">
                  {[0, 1, 2, 3, 4].map((i) => (
                    <span key={i} className="chat-wave-bar" style={{ '--wave-i': i } as CSSProperties} />
                  ))}
                </span>
                {m.status || 'Thinking…'}
              </p>
            ) : (
              <Markdown>{m.text}</Markdown>
            )}
          </div>

          {!user && m.reasoning && (
            <details className="mt-1.5 text-small text-text-muted">
              <summary className="w-fit cursor-pointer rounded-control select-none hover:text-text">Thinking</summary>
              <p className="mt-1 rounded-control bg-surface-alt px-3 py-2 whitespace-pre-wrap text-text">{m.reasoning}</p>
            </details>
          )}

          {!user && m.validation && m.validation.options.length > 0 && (
            <div className="mt-2 flex flex-wrap gap-2" aria-label="Answers">
              {m.validation.options.map((o, i) => {
                const label = optionLabel(o)
                return (
                  <Button key={`${label}-${i}`} size="sm" className="rounded-pill" disabled={!latest || busy} onClick={() => onAnswer?.(label)}>
                    {label}
                  </Button>
                )
              })}
            </div>
          )}

          {!user && m.actions && m.actions.length > 0 && (
            <div className="mt-2 flex flex-wrap items-center gap-2">
              {m.actionDone ? (
                <p className="text-small text-text-muted" role="status">
                  {m.actionDone}
                </p>
              ) : (
                m.actions.map((a) => (
                  <Button
                    key={a.button_action}
                    size="sm"
                    variant={actionVariant(a.button_type)}
                    disabled={running !== null}
                    loading={running === a.button_action}
                    onClick={() => void press(a)}
                  >
                    {a.button_text}
                  </Button>
                ))
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

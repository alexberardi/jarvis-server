import { useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

export interface TabItem {
  id: string
  label: ReactNode
  content: ReactNode
  disabled?: boolean
}

interface TabsProps {
  tabs: TabItem[]
  /** Names the tab list for screen readers. */
  label: string
  /** Controlled: the selected tab's id. */
  value?: string
  /** Uncontrolled: the first selected tab (the first enabled one otherwise). */
  defaultValue?: string
  onValueChange?: (id: string) => void
  className?: string
}

/**
 * Tabs follows the ARIA tabs pattern: one tab stop (roving tabindex), arrow keys, Home and End
 * move and select. The selection changes colour only; the panel swaps at once.
 */
export default function Tabs({ tabs, label, value, defaultValue, onValueChange, className }: TabsProps) {
  const base = useId()
  const enabled = tabs.filter((t) => !t.disabled)
  const [inner, setInner] = useState(defaultValue ?? enabled[0]?.id)
  const selected = value ?? inner
  const refs = useRef(new Map<string, HTMLButtonElement>())

  const select = (id: string) => {
    if (value === undefined) setInner(id)
    onValueChange?.(id)
  }

  const onKeyDown = (e: KeyboardEvent) => {
    const i = enabled.findIndex((t) => t.id === selected)
    const next = {
      ArrowRight: enabled[(i + 1) % enabled.length],
      ArrowLeft: enabled[(i - 1 + enabled.length) % enabled.length],
      Home: enabled[0],
      End: enabled[enabled.length - 1],
    }[e.key]
    if (!next) return
    e.preventDefault()
    select(next.id)
    refs.current.get(next.id)?.focus()
  }

  const current = tabs.find((t) => t.id === selected)
  return (
    <div className={className}>
      <div
        role="tablist"
        aria-label={label}
        onKeyDown={onKeyDown}
        className="flex max-w-full gap-1 overflow-x-auto border-b border-border"
      >
        {tabs.map((t) => {
          const on = t.id === selected
          return (
            <button
              key={t.id}
              ref={(el) => {
                if (el) refs.current.set(t.id, el)
                else refs.current.delete(t.id)
              }}
              type="button"
              role="tab"
              id={`${base}-tab-${t.id}`}
              aria-selected={on}
              aria-controls={`${base}-panel-${t.id}`}
              tabIndex={on ? 0 : -1}
              disabled={t.disabled}
              onClick={() => select(t.id)}
              className={cn(
                '-mb-px h-10 shrink-0 border-b-2 px-3 text-small font-medium whitespace-nowrap',
                'transition-colors duration-(--duration-hover) disabled:cursor-not-allowed disabled:opacity-50',
                on ? 'border-primary text-primary' : 'border-transparent text-text-muted hover:text-text',
              )}
            >
              {t.label}
            </button>
          )
        })}
      </div>
      {current && (
        <div
          role="tabpanel"
          id={`${base}-panel-${current.id}`}
          aria-labelledby={`${base}-tab-${current.id}`}
          tabIndex={0}
          className="pt-4"
        >
          {current.content}
        </div>
      )}
    </div>
  )
}

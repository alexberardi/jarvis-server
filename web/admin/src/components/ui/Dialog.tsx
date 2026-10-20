import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { X } from 'lucide-react'
import { cn } from '@/lib/utils'
import Button from './Button'

export interface DialogProps {
  open: boolean
  /** Escape, the backdrop, the close button. */
  onClose: () => void
  title: ReactNode
  description?: ReactNode
  children?: ReactNode
  /** The actions row at the bottom (Cancel, Save). */
  footer?: ReactNode
  /** `sheet` comes up from the bottom on a phone and is a centred dialog from md. */
  variant?: 'dialog' | 'sheet'
  /** False while closing would leave the user nowhere (an action that can't be undone is under way): no close button, Escape and the backdrop do nothing. */
  dismissible?: boolean
  className?: string
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

// The exit animation's length (--duration-menu); the dialog unmounts after it.
const EXIT_MS = 160

/**
 * Dialog is a modal: a portal over the page with a backdrop, focus kept inside, Escape and the
 * backdrop to close, and focus back on what opened it. It enters with opacity and a 4px rise in
 * 160ms (a sheet slides up in 220ms on the drawer curve) and leaves the same way; reduced motion
 * leaves only the fade.
 */
export default function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  variant = 'dialog',
  dismissible = true,
  className,
}: DialogProps) {
  const [mounted, setMounted] = useState(open)
  if (open && !mounted) setMounted(true)

  // Unmount once the exit animation has played.
  useEffect(() => {
    if (open || !mounted) return
    const t = setTimeout(() => setMounted(false), EXIT_MS)
    return () => clearTimeout(t)
  }, [open, mounted])

  if (!mounted) return null
  return createPortal(
    <DialogPanel
      open={open}
      onClose={onClose}
      title={title}
      description={description}
      footer={footer}
      variant={variant}
      dismissible={dismissible}
      className={className}
    >
      {children}
    </DialogPanel>,
    document.body,
  )
}

/** Sheet is a Dialog that comes up from the bottom of a phone screen. */
export function Sheet(props: Omit<DialogProps, 'variant'>) {
  return <Dialog {...props} variant="sheet" />
}

function DialogPanel({ open, onClose: close, title, description, children, footer, variant, dismissible, className }: DialogProps) {
  const onClose = () => {
    if (dismissible) close()
  }
  const titleId = useId()
  const descriptionId = useId()
  const panel = useRef<HTMLDivElement>(null)

  // Focus moves in on open and back to the opener on close.
  useEffect(() => {
    if (!open) return
    const opener = document.activeElement as HTMLElement | null
    const first = panel.current?.querySelector<HTMLElement>('[autofocus], [data-autofocus]') ??
      panel.current?.querySelector('[data-dialog-body]')?.querySelector<HTMLElement>(FOCUSABLE) ??
      panel.current
    first?.focus()
    const { overflow } = document.body.style
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = overflow
      if (opener?.isConnected) opener.focus()
    }
  }, [open])

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
      return
    }
    if (e.key !== 'Tab' || !panel.current) return
    const items = [...panel.current.querySelectorAll<HTMLElement>(FOCUSABLE)]
    if (items.length === 0) {
      e.preventDefault()
      return
    }
    const [first, last] = [items[0], items[items.length - 1]]
    const active = document.activeElement
    if (e.shiftKey && (active === first || active === panel.current)) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && active === last) {
      e.preventDefault()
      first.focus()
    }
  }

  const sheet = variant === 'sheet'
  const state = open ? 'open' : 'closed'
  return (
    <div
      className={cn('fixed inset-0 z-50 flex justify-center', sheet ? 'items-end md:items-center md:p-4' : 'items-center p-4')}
      onKeyDown={onKeyDown}
    >
      <div
        aria-hidden
        data-testid="dialog-backdrop"
        className={cn('absolute inset-0 bg-black/50', open ? 'animate-fade-in' : 'animate-fade-out')}
        onClick={onClose}
      />
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
        data-state={state}
        className={cn(
          'relative flex max-h-[calc(100dvh-2rem)] w-full max-w-md flex-col border border-border bg-surface text-text shadow-overlay outline-none',
          sheet
            ? cn(
                'max-h-[90dvh] rounded-t-card pb-[env(safe-area-inset-bottom)] md:max-h-[calc(100dvh-2rem)] md:rounded-card md:pb-0',
                open ? 'animate-sheet-in md:animate-pop-in' : 'animate-sheet-out md:animate-pop-out',
              )
            : cn('rounded-card', open ? 'animate-pop-in' : 'animate-pop-out'),
          className,
        )}
      >
        <div className="flex items-start gap-3 px-4 pt-4 md:px-5 md:pt-5">
          <div className="min-w-0 flex-1">
            <h2 id={titleId} className="text-heading">
              {title}
            </h2>
            {description && (
              <p id={descriptionId} className="mt-1 text-small text-text-muted">
                {description}
              </p>
            )}
          </div>
          {dismissible && (
            <Button variant="ghost" size="icon" aria-label="Close" onClick={onClose} className="-mt-1 -mr-2">
              <X size={18} aria-hidden />
            </Button>
          )}
        </div>
        <div data-dialog-body className="min-h-0 flex-1 overflow-y-auto px-4 py-4 md:px-5">
          {children}
        </div>
        {footer && (
          <div className="flex flex-wrap justify-end gap-2 border-t border-border px-4 py-3 md:px-5">{footer}</div>
        )}
      </div>
    </div>
  )
}

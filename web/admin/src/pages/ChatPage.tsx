import { type ChangeEvent, type ClipboardEvent, type DragEvent, type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from 'react'
import { ImagePlus, Plus, SendHorizontal, Square, WifiOff, X } from 'lucide-react'
import { toast } from 'sonner'
import { useAuth } from '@/hooks/useAuth'
import { useChat } from '@/hooks/useChat'
import { nodeLabel } from '@/lib/chatNode'
import { prepareImage, type PendingImage } from '@/lib/chatImages'
import { errorMessage } from '@/lib/errors'
import { nameOf } from '@/lib/session'
import { cn } from '@/lib/utils'
import MessageBubble from '@/components/chat/MessageBubble'

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

/** Suggestions on an empty conversation (jarvis-web's quick actions). */
const QUICK = [
  "What's the weather?",
  'Set a timer for 5 minutes',
  "What's on my calendar today?",
  'What is 15% of 85?',
  'Tell me a joke',
  "What's in the news today?",
]

/**
 * ChatPage is home (webapp 01 §3): type to Jarvis through a node of the active household. The
 * input stays front and centre; there is no history list (Q5), the open conversation lives in
 * this tab.
 */
export default function ChatPage() {
  const { state, activeHouseholdId } = useAuth()
  const user = state.user
  const chat = useChat(user?.id ?? 0, activeHouseholdId)
  const [text, setText] = useState('')
  const [images, setImages] = useState<PendingImage[]>([])
  const [dragging, setDragging] = useState(false)
  const listRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  const imagesRef = useRef(images)
  useEffect(() => {
    imagesRef.current = images
  }, [images])

  // Follow the conversation as it grows.
  const last = chat.messages[chat.messages.length - 1]
  useEffect(() => {
    const el = listRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [chat.messages.length, last?.text, last?.status])

  // Previews are object URLs: let them go with the page.
  useEffect(() => () => imagesRef.current.forEach((i) => URL.revokeObjectURL(i.previewUrl)), [])

  function clearImages() {
    images.forEach((i) => URL.revokeObjectURL(i.previewUrl))
    setImages([])
  }

  async function addFiles(files: FileList | File[] | null) {
    if (!files || !chat.imagesEnabled) return
    const list = Array.from(files).filter((f) => f.type.startsWith('image/'))
    const room = chat.maxImages - images.length
    if (list.length > room) toast.error(`Up to ${chat.maxImages} photos per message`)
    for (const f of list.slice(0, Math.max(0, room))) {
      try {
        const img = await prepareImage(f, chat.maxImageBytes)
        setImages((cur) => [...cur, img])
      } catch (err) {
        toast.error(errorMessage(err, 'Could not add that photo'))
      }
    }
  }

  function submit(e?: FormEvent) {
    e?.preventDefault()
    if (chat.busy || (!text.trim() && images.length === 0)) return
    chat.send(
      text,
      images.map((i) => i.payload),
    )
    setText('')
    clearImages()
    inputRef.current?.focus()
  }

  function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault()
      submit()
    }
  }

  function onPaste(e: ClipboardEvent<HTMLTextAreaElement>) {
    if (!chat.imagesEnabled) return
    const files = Array.from(e.clipboardData.files)
    if (files.some((f) => f.type.startsWith('image/'))) {
      e.preventDefault()
      void addFiles(files)
    }
  }

  function onDrop(e: DragEvent) {
    setDragging(false)
    if (!chat.imagesEnabled) return
    e.preventDefault()
    void addFiles(e.dataTransfer.files)
  }

  const node = chat.node
  const name = nameOf(user)
  const noNodes = !chat.nodesLoading && !chat.nodesError && chat.nodes.length === 0

  return (
    <div
      className="-m-4 flex h-[calc(100%+2rem)] min-h-0 flex-col md:-m-6 md:h-[calc(100%+3rem)]"
      onDragOver={(e) => {
        if (chat.imagesEnabled) {
          e.preventDefault()
          setDragging(true)
        }
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={onDrop}
    >
      {/* Node picker and new conversation */}
      <div className="flex shrink-0 items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 md:px-6">
        <label htmlFor="chat-node" className="sr-only">
          Device
        </label>
        <div className="relative min-w-0 flex-1 sm:flex-none">
          <select
            id="chat-node"
            value={node?.node_id ?? ''}
            disabled={chat.nodes.length === 0}
            onChange={(e) => chat.pickNode(e.target.value)}
            className="h-9 w-full min-w-0 truncate rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] pr-2 pl-6 text-sm text-[var(--color-text)] sm:w-64"
          >
            {chat.nodes.length === 0 && <option value="">{chat.nodesLoading ? 'Loading devices...' : 'No devices'}</option>}
            {chat.nodes.map((n) => (
              <option key={n.node_id} value={n.node_id}>
                {nodeLabel(n)}
                {n.node_kind === 'desktop' ? ' (computer)' : ''}
                {n.online ? '' : ' - offline'}
              </option>
            ))}
          </select>
          <span
            aria-hidden="true"
            className={cn(
              'pointer-events-none absolute top-1/2 left-2.5 h-2 w-2 -translate-y-1/2 rounded-full',
              node?.online ? 'bg-green-500' : 'bg-[var(--color-text-muted)]',
            )}
          />
        </div>
        <button
          type="button"
          onClick={() => {
            chat.newConversation()
            clearImages()
            inputRef.current?.focus()
          }}
          disabled={chat.messages.length === 0}
          className="inline-flex h-9 shrink-0 items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:opacity-50"
        >
          <Plus size={16} aria-hidden="true" />
          <span className="max-sm:sr-only">New conversation</span>
        </button>
      </div>

      {node && !node.online && (
        <div role="status" className="flex shrink-0 items-start gap-2 bg-amber-500/10 px-4 py-2 text-sm text-amber-700 md:px-6 dark:text-amber-300">
          <WifiOff size={16} className="mt-0.5 shrink-0" aria-hidden="true" />
          <span>
            {capitalize(nodeLabel(node))} is offline: questions work, commands that run on it won't.
          </span>
        </div>
      )}
      {chat.nodesError && (
        <div role="alert" className="shrink-0 bg-red-500/10 px-4 py-2 text-sm text-red-600 md:px-6 dark:text-red-400">
          {chat.nodesError}
        </div>
      )}

      {/* Conversation */}
      <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto px-3 py-4 md:px-6" aria-live="polite">
        {chat.messages.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center px-2 text-center">
            <p className="mb-5 text-lg text-[var(--color-text-muted)]">
              Ask <span className="font-semibold text-[var(--color-primary)]">Jarvis</span> anything
            </p>
            {noNodes ? (
              <p className="max-w-sm text-sm text-[var(--color-text-muted)]">
                Your household has no devices yet. Add a computer or a voice node, and chat with Jarvis through it.
              </p>
            ) : (
              <div className="flex max-w-lg flex-wrap justify-center gap-2">
                {QUICK.map((q) => (
                  <button
                    key={q}
                    type="button"
                    disabled={!node || chat.busy}
                    onClick={() => chat.send(q)}
                    className="rounded-full border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text-muted)] transition-colors hover:border-[var(--color-primary)] hover:text-[var(--color-primary)] disabled:opacity-50"
                  >
                    {q}
                  </button>
                ))}
              </div>
            )}
          </div>
        ) : (
          <div className="mx-auto flex max-w-3xl flex-col gap-3">
            {chat.messages.map((m, i) => (
              <MessageBubble
                key={m.id}
                message={m}
                userName={name}
                latest={i === chat.messages.length - 1}
                busy={chat.busy}
                onAnswer={(t) => chat.send(t)}
                onAction={(a) => chat.runAction(m.id, a)}
              />
            ))}
          </div>
        )}
      </div>

      {/* Composer */}
      <form
        onSubmit={submit}
        className={cn(
          'shrink-0 border-t border-[var(--color-border)] bg-[var(--color-surface)] px-3 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] md:px-6',
          dragging && 'ring-2 ring-[var(--color-primary)] ring-inset',
        )}
      >
        {images.length > 0 && (
          <ul className="mx-auto mb-2 flex max-w-3xl gap-2" aria-label="Photos to send">
            {images.map((img) => (
              <li key={img.id} className="relative">
                <img src={img.previewUrl} alt="" className="h-14 w-14 rounded-lg border border-[var(--color-border)] object-cover" />
                <button
                  type="button"
                  aria-label="Remove photo"
                  onClick={() => {
                    URL.revokeObjectURL(img.previewUrl)
                    setImages((cur) => cur.filter((c) => c.id !== img.id))
                  }}
                  className="absolute -top-1.5 -right-1.5 inline-flex h-5 w-5 items-center justify-center rounded-full bg-black/70 text-white"
                >
                  <X size={12} />
                </button>
              </li>
            ))}
          </ul>
        )}
        <div className="mx-auto flex max-w-3xl items-end gap-2">
          {chat.imagesEnabled && (
            <>
              <input
                ref={fileRef}
                type="file"
                accept="image/*"
                multiple
                hidden
                data-testid="chat-file"
                onChange={(e: ChangeEvent<HTMLInputElement>) => {
                  void addFiles(e.target.files)
                  e.target.value = ''
                }}
              />
              <button
                type="button"
                onClick={() => fileRef.current?.click()}
                disabled={images.length >= chat.maxImages}
                aria-label="Add a photo"
                title="Add a photo"
                className="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] disabled:opacity-50"
              >
                <ImagePlus size={20} />
              </button>
            </>
          )}
          <label htmlFor="chat-input" className="sr-only">
            Message
          </label>
          <textarea
            id="chat-input"
            ref={inputRef}
            rows={1}
            autoFocus
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
            onPaste={onPaste}
            placeholder={node ? 'Ask Jarvis...' : 'No device to chat through'}
            disabled={!node}
            maxLength={5000}
            className="max-h-40 min-h-10 min-w-0 flex-1 resize-none rounded-xl border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2 text-sm text-[var(--color-text)] outline-none [field-sizing:content] placeholder:text-[var(--color-text-muted)] focus:ring-2 focus:ring-[var(--color-primary)] disabled:opacity-60"
          />
          {chat.busy ? (
            <button
              type="button"
              onClick={chat.stop}
              aria-label="Stop"
              title="Stop"
              className="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg border border-[var(--color-border)] text-[var(--color-text)] hover:bg-[var(--color-surface-alt)]"
            >
              <Square size={16} />
            </button>
          ) : (
            <button
              type="submit"
              aria-label="Send"
              title="Send"
              disabled={!node || (!text.trim() && images.length === 0)}
              className="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-[var(--color-primary)] text-white hover:opacity-90 disabled:opacity-50"
            >
              <SendHorizontal size={18} />
            </button>
          )}
        </div>
      </form>
    </div>
  )
}

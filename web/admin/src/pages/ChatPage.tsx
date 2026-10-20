import { type ChangeEvent, type ClipboardEvent, type DragEvent, type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from 'react'
import { AlertTriangle, ImagePlus, MessageCircle, Plus, SendHorizontal, Square, WifiOff, X } from 'lucide-react'
import { toast } from 'sonner'
import { useAuth } from '@/hooks/useAuth'
import { useChat } from '@/hooks/useChat'
import { nodeLabel } from '@/lib/chatNode'
import { prepareImage, type PendingImage } from '@/lib/chatImages'
import { errorMessage } from '@/lib/errors'
import { nameOf } from '@/lib/session'
import { cn } from '@/lib/utils'
import MessageBubble from '@/components/chat/MessageBubble'
import { Banner, Button, EmptyState, Select } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'

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
  const canSend = !!node && (text.trim() !== '' || images.length > 0)
  const nodeOptions =
    chat.nodes.length === 0
      ? [{ value: '', label: chat.nodesLoading ? 'Loading devices…' : 'No devices' }]
      : chat.nodes.map((n) => ({
          value: n.node_id,
          label:
            nodeLabel(n) +
            (n.node_id === chat.thisDeviceId ? ' (this computer)' : n.node_kind === 'desktop' ? ' (computer)' : '') +
            (n.online ? '' : ' - offline'),
        }))

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
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-surface px-3 py-2 md:px-6">
        <div className="relative min-w-0 flex-1 sm:w-72 sm:flex-none">
          <Select
            id="chat-node"
            aria-label="Device"
            value={node?.node_id ?? ''}
            disabled={chat.nodes.length === 0}
            onValueChange={(v) => chat.pickNode(v)}
            options={nodeOptions}
            className="truncate pl-8"
          />
          <span
            aria-hidden="true"
            className={cn('pointer-events-none absolute top-1/2 left-3 size-2 -translate-y-1/2 rounded-pill', node?.online ? 'bg-success' : 'bg-text-muted')}
          />
        </div>
        <Button
          variant="ghost"
          icon={<Plus size={16} aria-hidden="true" />}
          onClick={() => {
            chat.newConversation()
            clearImages()
            inputRef.current?.focus()
          }}
          disabled={chat.messages.length === 0}
          className="ml-auto"
        >
          <span className="max-sm:sr-only">New conversation</span>
        </Button>
      </div>

      {node && !node.online && (
        <Banner role="status" tone="warning" icon={<WifiOff size={16} />} className="shrink-0 rounded-none border-x-0 border-t-0 px-4 py-2 md:px-6">
          {capitalize(nodeLabel(node))} is offline: questions work, commands that run on it won't.
        </Banner>
      )}
      {chat.nodesError && (
        <Banner role="alert" tone="danger" icon={<AlertTriangle size={16} />} className="shrink-0 rounded-none border-x-0 border-t-0 px-4 py-2 md:px-6">
          {chat.nodesError}
        </Banner>
      )}

      {/* Conversation */}
      <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto px-3 py-4 md:px-6" aria-live="polite">
        {chat.messages.length === 0 ? (
          <div className="flex h-full items-center justify-center">
            <EmptyState
              icon={<MessageCircle size={20} />}
              title="Ask Jarvis anything"
              description={
                noNodes
                  ? 'Your household has no devices yet. Add a computer or a voice node, and chat with Jarvis through it.'
                  : 'Type below, or start with one of these.'
              }
              action={
                !noNodes && (
                  <div className="flex max-w-lg flex-wrap justify-center gap-2">
                    {QUICK.map((q) => (
                      <Button key={q} size="sm" disabled={!node || chat.busy} onClick={() => chat.send(q)} className="rounded-pill font-normal">
                        {q}
                      </Button>
                    ))}
                  </div>
                )
              }
            />
          </div>
        ) : (
          <div className="mx-auto flex max-w-3xl flex-col gap-4">
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
          'shrink-0 border-t border-border bg-surface px-3 pt-2 pb-[max(0.5rem,env(safe-area-inset-bottom))] md:px-6',
          dragging && 'ring-2 ring-primary ring-inset',
        )}
      >
        {images.length > 0 && (
          <ul className="mx-auto mb-2 flex max-w-3xl gap-2" aria-label="Photos to send">
            {images.map((img) => (
              <li key={img.id} className="relative">
                <img src={img.previewUrl} alt="" className="size-14 rounded-control border border-border object-cover" />
                <button
                  type="button"
                  aria-label="Remove photo"
                  onClick={() => {
                    URL.revokeObjectURL(img.previewUrl)
                    setImages((cur) => cur.filter((c) => c.id !== img.id))
                  }}
                  className="absolute -top-2 -right-2 inline-flex size-6 items-center justify-center rounded-pill bg-text text-surface"
                >
                  <X size={14} aria-hidden="true" />
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
              <Button
                variant="ghost"
                size="icon"
                onClick={() => fileRef.current?.click()}
                disabled={images.length >= chat.maxImages}
                aria-label="Add a photo"
                title="Add a photo"
                icon={<ImagePlus size={20} aria-hidden="true" />}
              />
            </>
          )}
          <label htmlFor="chat-input" className="sr-only">
            Message
          </label>
          {/* The composer is focused most of the time, so its focus is the border turning purple, not the 2px ring every other control gets. */}
          <textarea
            id="chat-input"
            ref={inputRef}
            rows={1}
            autoFocus
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
            onPaste={onPaste}
            placeholder={node ? 'Ask Jarvis' : 'No device to chat through'}
            disabled={!node}
            maxLength={5000}
            className={cn(fieldControl, 'h-auto max-h-40 min-h-10 flex-1 resize-none py-2 [field-sizing:content] focus:border-primary focus-visible:outline-none')}
          />
          {chat.busy ? (
            <Button size="icon" onClick={chat.stop} aria-label="Stop" title="Stop" icon={<Square size={16} aria-hidden="true" />} />
          ) : (
            // Filled only when there is something to send: a pale purple would look pressed, not off.
            <Button
              type="submit"
              variant={canSend ? 'primary' : 'secondary'}
              size="icon"
              aria-label="Send"
              title="Send"
              disabled={!canSend}
              icon={<SendHorizontal size={18} aria-hidden="true" />}
            />
          )}
        </div>
      </form>
    </div>
  )
}

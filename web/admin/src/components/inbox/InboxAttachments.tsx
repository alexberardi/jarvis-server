import { useEffect, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { fetchAttachment } from '@/api/inbox'
import type { InboxAttachment } from '@/api/inbox'

function Attachment({ itemId, a, n }: { itemId: string; a: InboxAttachment; n: number }) {
  const [url, setUrl] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let live = true
    let made: string | null = null
    fetchAttachment(itemId, a.id)
      .then((blob) => {
        if (!live) return
        made = URL.createObjectURL(blob)
        setUrl(made)
      })
      .catch(() => live && setFailed(true))
    return () => {
      live = false
      if (made) URL.revokeObjectURL(made)
    }
  }, [itemId, a.id])

  const label = `Attachment ${n}`
  const box = 'max-h-[70vh] max-w-full rounded-lg border border-[var(--color-border)]'
  if (failed)
    return (
      <p role="alert" className="text-sm text-red-500">
        Could not load {label.toLowerCase()}.
      </p>
    )
  if (!url)
    return (
      <div
        className="flex items-center justify-center rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] text-[var(--color-text-muted)]"
        style={{ aspectRatio: a.width && a.height ? `${a.width} / ${a.height}` : '16 / 9', maxHeight: '70vh' }}
      >
        <Loader2 size={18} className="animate-spin" aria-label={`Loading ${label.toLowerCase()}`} />
      </div>
    )
  if (a.mime.startsWith('video/'))
    return <video src={url} controls preload="metadata" aria-label={label} className={`${box} w-full bg-black`} />
  return <img src={url} alt={label} width={a.width ?? undefined} height={a.height ?? undefined} className={`${box} h-auto`} />
}

/** InboxAttachments shows an item's images and plays its videos, in order. */
export default function InboxAttachments({ itemId, attachments }: { itemId: string; attachments: InboxAttachment[] }) {
  if (!attachments.length) return null
  return (
    <section aria-label="Attachments" className="space-y-3">
      {attachments.map((a, i) => (
        <Attachment key={a.id} itemId={itemId} a={a} n={i + 1} />
      ))}
    </section>
  )
}

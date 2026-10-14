import type { ChatImagePayload } from '@/api/chat'

/** The long side a photo is downscaled to before it is sent (webapp 01 §3.1). */
export const MAX_SIDE = 1024

/** A photo waiting in the composer: a preview URL plus what goes over the wire. */
export interface PendingImage {
  id: string
  previewUrl: string
  payload: ChatImagePayload
}

/** fitWithin scales width × height so the long side is at most max, never upscaling. */
export function fitWithin(width: number, height: number, max = MAX_SIDE): { width: number; height: number } {
  const long = Math.max(width, height)
  if (long <= max || long === 0) return { width, height }
  const k = max / long
  return { width: Math.max(1, Math.round(width * k)), height: Math.max(1, Math.round(height * k)) }
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error('That file is not an image the browser can read'))
    img.src = url
  })
}

function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => {
      const s = String(r.result)
      resolve(s.slice(s.indexOf(',') + 1))
    }
    r.onerror = () => reject(r.error ?? new Error('Could not read the image'))
    r.readAsDataURL(blob)
  })
}

/**
 * prepareImage downscales a photo to about MAX_SIDE px as JPEG, stepping the quality down until
 * it fits cc's per-image byte limit. The preview is an object URL (CSP img-src blob:) the
 * caller revokes when the photo is sent or removed.
 */
export async function prepareImage(file: File, maxBytes: number): Promise<PendingImage> {
  const src = URL.createObjectURL(file)
  try {
    const img = await loadImage(src)
    const { width, height } = fitWithin(img.naturalWidth, img.naturalHeight)
    const canvas = document.createElement('canvas')
    canvas.width = width
    canvas.height = height
    const ctx = canvas.getContext('2d')
    if (!ctx) throw new Error('This browser cannot resize images')
    ctx.drawImage(img, 0, 0, width, height)
    let blob: Blob | null = null
    for (const q of [0.85, 0.7, 0.55, 0.4]) {
      blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, 'image/jpeg', q))
      if (blob && blob.size <= maxBytes) break
    }
    if (!blob || blob.size > maxBytes) throw new Error('That photo is too large to send')
    const data = await blobToBase64(blob)
    return {
      id: Math.random().toString(36).slice(2),
      previewUrl: URL.createObjectURL(blob),
      payload: { mime: 'image/jpeg', data },
    }
  } finally {
    URL.revokeObjectURL(src)
  }
}

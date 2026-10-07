/** One server-sent event. `id` is the `id:` field when the event carried one. */
export interface SSEEvent {
  id?: string
  event?: string
  data: string
}

/**
 * SSEParser turns a text/event-stream, fed in arbitrary chunks, into events (the WHATWG
 * framing: fields per line, a blank line ends an event, `:` lines are comments). Comments are
 * handed to `onComment`, because jarvisd's log tail announces where it starts in one.
 */
export class SSEParser {
  private buf = ''
  private data: string[] = []
  private id: string | undefined
  private event: string | undefined

  private onComment?: (text: string) => void

  constructor(onComment?: (text: string) => void) {
    this.onComment = onComment
  }

  feed(chunk: string): SSEEvent[] {
    this.buf += chunk
    const out: SSEEvent[] = []
    let nl: number
    while ((nl = this.buf.search(/\r\n|\r|\n/)) >= 0) {
      const line = this.buf.slice(0, nl)
      const sep = this.buf.startsWith('\r\n', nl) ? 2 : 1
      this.buf = this.buf.slice(nl + sep)
      if (line === '') {
        if (this.data.length > 0) out.push({ id: this.id, event: this.event, data: this.data.join('\n') })
        this.data = []
        this.id = undefined
        this.event = undefined
        continue
      }
      if (line.startsWith(':')) {
        this.onComment?.(line.slice(1).trimStart())
        continue
      }
      const colon = line.indexOf(':')
      const field = colon < 0 ? line : line.slice(0, colon)
      let value = colon < 0 ? '' : line.slice(colon + 1)
      if (value.startsWith(' ')) value = value.slice(1)
      if (field === 'data') this.data.push(value)
      else if (field === 'id') this.id = value
      else if (field === 'event') this.event = value
    }
    return out
  }
}

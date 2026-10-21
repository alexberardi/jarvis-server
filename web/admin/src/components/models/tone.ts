import type { BadgeTone } from '@/components/ui'
import { stateTone, type Tone } from './styles'

/** badgeTone maps the Models components' tones onto Badge's status tones. */
export const badgeTone: Record<Tone, BadgeTone> = {
  ok: 'success',
  warn: 'warning',
  bad: 'danger',
  info: 'primary',
  muted: 'neutral',
}

/** stateBadge is the Badge tone of an engine/label/install state. */
export function stateBadge(state: string): BadgeTone {
  return badgeTone[stateTone(state)]
}

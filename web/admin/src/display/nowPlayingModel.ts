// What plays on this node, from any package (docs/agent/PLAYBACK.md piece 4): the playback hub's
// state. Nothing here names a service; a button shows only when the source lists its action.
export type NowPlaying = {
  source: string
  name: string
  updated_at: string
  state: {
    state: 'playing' | 'paused' | 'idle'
    media_title: string
    media_artist: string
    media_album: string
    context: string
    actions: string[]
    position?: number
    duration?: number
    volume?: number
    shuffle?: boolean
    repeat?: 'off' | 'all' | 'one'
    liked?: boolean
  }
}
export type Send = (body: { action: string; volume?: number; position?: number }) => void

// trackKey changes with the track, so a new cover is fetched and Home's dismissal ends.
export const trackKey = (n: NowPlaying) => [n.source, n.state.media_title, n.state.media_artist, n.state.media_album].join('\u0000')

// position advances on the display's own clock between the node's readings.
export function position(n: NowPlaying, now: Date) {
  const at = n.state.position ?? 0
  if (n.state.state !== 'playing') return at
  const elapsed = Math.max(0, (now.getTime() - new Date(n.updated_at).getTime()) / 1000)
  return n.state.duration ? Math.min(n.state.duration, at + elapsed) : at + elapsed
}

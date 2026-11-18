import { useEffect, useState } from 'react'
import { Heart, Music, Pause, Play, Repeat, Repeat1, Shuffle, SkipBack, SkipForward, Volume2 } from 'lucide-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { controlPlayback, fetchCover, getPlayback, type NodePlayback } from '@/api/playback'
import { position } from '@/display/nowPlayingModel'
import { Alert, Button, Card, EmptyState, ErrorState, PageHeader, SkeletonList } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

// As mobile: fast while something plays, slow otherwise (react-query pauses in a hidden tab).
const PLAYING_POLL_MS = 2000
const IDLE_POLL_MS = 15000

type Control = { node_id: string; action: string; volume?: number; position?: number }

const clock = (s: number) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, '0')}`

/**
 * NowPlayingPage is everything playing in the household (mobile's Now playing), one card per
 * node, with only the controls its source lists. A control's answer is the node's read-back and
 * replaces that card at once.
 */
export default function NowPlayingPage() {
  const { activeHouseholdId: hh } = useAuth()
  const qc = useQueryClient()
  const key = ['playback', hh]
  const q = useQuery({
    queryKey: key,
    queryFn: () => getPlayback(hh),
    enabled: !!hh,
    retry: false,
    refetchInterval: (query) => ((query.state.data?.length ?? 0) > 0 ? PLAYING_POLL_MS : IDLE_POLL_MS),
  })
  const control = useMutation({
    mutationFn: (c: Control) => controlPlayback(c),
    onSuccess: (now, c) =>
      qc.setQueryData<NodePlayback[]>(key, (old = []) => {
        if (!now) return old.filter((p) => p.node_id !== c.node_id)
        return old.some((p) => p.node_id === c.node_id) ? old.map((p) => (p.node_id === c.node_id ? now : p)) : [...old, now]
      }),
  })

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <PageHeader title="Now playing" description="What’s playing on the household’s speakers and computers." />
      {control.isError && (
        <Alert tone="danger" title="That didn’t work">
          {errorMessage(control.error, 'The node may be offline.')}
        </Alert>
      )}
      {q.isError ? (
        <Card flush>
          <ErrorState
            title="Couldn't load what's playing"
            message={errorMessage(q.error, 'Check your connection and try again.')}
            onRetry={() => q.refetch()}
          />
        </Card>
      ) : q.isLoading ? (
        <Card flush>
          <SkeletonList rows={2} label="Loading what's playing" />
        </Card>
      ) : !q.data?.length ? (
        <Card flush>
          <EmptyState
            icon={<Music size={20} />}
            title="Nothing is playing"
            description="Ask Jarvis to play something, and it shows up here with its controls."
          />
        </Card>
      ) : (
        q.data.map((p) => (
          <PlayerCard
            key={p.node_id}
            p={p}
            busy={control.isPending && control.variables?.node_id === p.node_id}
            send={(c) => control.mutate({ node_id: p.node_id, ...c })}
          />
        ))
      )}
    </div>
  )
}

function Cover({ p }: { p: NodePlayback }) {
  const [url, setUrl] = useState<string | null>(null)
  useEffect(() => {
    if (!p.cover) return
    let live = true
    let made: string | null = null
    fetchCover(p)
      .then((blob) => {
        if (!live) return
        made = URL.createObjectURL(blob)
        setUrl(made)
      })
      .catch(() => live && setUrl(null))
    return () => {
      live = false
      if (made) URL.revokeObjectURL(made)
    }
    // A new cover key is a new image; the rest of p changes every poll.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.node_id, p.cover])
  const box = 'size-24 shrink-0 rounded-card bg-surface-alt'
  if (!p.cover || !url)
    return (
      <span className={cn(box, 'flex items-center justify-center text-text-muted')} aria-hidden>
        <Music size={32} />
      </span>
    )
  return <img src={url} alt={p.state.media_album ? `Cover of ${p.state.media_album}` : 'Cover'} className={cn(box, 'object-cover')} />
}

function PlayerCard({ p, busy, send }: { p: NodePlayback; busy: boolean; send: (c: Omit<Control, 'node_id'>) => void }) {
  const s = p.state
  const can = (a: string) => s.actions.includes(a)
  const playing = s.state === 'playing'
  const [now, setNow] = useState(() => new Date())
  // A drag on the seek or volume slider, held until it's let go.
  const [seek, setSeek] = useState<number | null>(null)
  const [volume, setVolume] = useState<number | null>(null)
  useEffect(() => {
    if (!playing || s.duration === undefined) return
    const t = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(t)
  }, [playing, s.duration])
  const at = seek ?? position(p, now)
  const repeatLabel = s.repeat === 'one' ? 'Repeat one' : s.repeat === 'all' ? 'Repeat all' : 'Repeat off'
  const commitSeek = () => {
    if (seek === null) return
    send({ action: 'seek', position: seek })
    setSeek(null)
  }
  const commitVolume = () => {
    if (volume === null) return
    send({ action: 'volume', volume })
    setVolume(null)
  }
  const toggle = 'aria-pressed:bg-primary-soft aria-pressed:text-primary'

  return (
    <Card as="section" aria-label={`${p.node_name}: ${s.media_title || 'Unknown track'}`} className="space-y-4">
      <p className="text-small text-text-muted">
        {p.node_name} · {s.context || p.name}
      </p>
      <div className="flex items-center gap-4">
        <Cover p={p} />
        <div className="min-w-0 space-y-0.5">
          <p className="text-subheading text-pretty text-text">{s.media_title || 'Unknown track'}</p>
          {s.media_artist && <p className="truncate text-body text-text-muted">{s.media_artist}</p>}
          {s.state === 'paused' && <p className="text-small text-text-muted">Paused</p>}
        </div>
      </div>
      {s.duration !== undefined && s.position !== undefined && (
        <div className="space-y-1">
          <input
            type="range"
            aria-label="Position"
            aria-valuetext={`${clock(at)} of ${clock(s.duration)}`}
            min={0}
            max={s.duration}
            step={1}
            value={Math.round(at)}
            disabled={!can('seek') || busy}
            onChange={(e) => setSeek(Number(e.target.value))}
            onPointerUp={commitSeek}
            onKeyUp={commitSeek}
            className="w-full accent-primary disabled:opacity-60"
          />
          <div className="flex justify-between text-small text-text-muted tabular-nums">
            <span>{clock(at)}</span>
            <span>{clock(s.duration)}</span>
          </div>
        </div>
      )}
      <div className="flex flex-wrap items-center justify-center gap-1">
        {s.shuffle !== undefined && can('shuffle') && (
          <Button
            size="icon"
            variant="ghost"
            aria-label="Shuffle"
            aria-pressed={s.shuffle}
            className={toggle}
            disabled={busy}
            onClick={() => send({ action: 'shuffle' })}
          >
            <Shuffle size={18} aria-hidden />
          </Button>
        )}
        {can('previous') && (
          <Button size="icon" variant="ghost" aria-label="Previous track" disabled={busy} onClick={() => send({ action: 'previous' })}>
            <SkipBack size={20} aria-hidden />
          </Button>
        )}
        {(playing ? can('pause') : can('play')) && (
          <Button
            size="icon"
            variant="primary"
            aria-label={playing ? 'Pause' : 'Play'}
            className="size-12 rounded-pill"
            disabled={busy}
            onClick={() => send({ action: playing ? 'pause' : 'play' })}
          >
            {playing ? <Pause size={22} aria-hidden /> : <Play size={22} aria-hidden />}
          </Button>
        )}
        {can('next') && (
          <Button size="icon" variant="ghost" aria-label="Next track" disabled={busy} onClick={() => send({ action: 'next' })}>
            <SkipForward size={20} aria-hidden />
          </Button>
        )}
        {s.repeat !== undefined && can('repeat') && (
          <Button
            size="icon"
            variant="ghost"
            aria-label={repeatLabel}
            aria-pressed={s.repeat !== 'off'}
            className={toggle}
            disabled={busy}
            onClick={() => send({ action: 'repeat' })}
          >
            {s.repeat === 'one' ? <Repeat1 size={18} aria-hidden /> : <Repeat size={18} aria-hidden />}
          </Button>
        )}
        {s.liked !== undefined && can('like') && (
          <Button
            size="icon"
            variant="ghost"
            aria-label={s.liked ? 'Remove from liked' : 'Like this track'}
            aria-pressed={s.liked}
            className={toggle}
            disabled={busy}
            onClick={() => send({ action: 'like' })}
          >
            <Heart size={18} aria-hidden className={cn(s.liked && 'fill-current')} />
          </Button>
        )}
      </div>
      {s.volume !== undefined && can('volume') && (
        <label className="flex items-center gap-3">
          <Volume2 size={18} aria-hidden className="shrink-0 text-text-muted" />
          <span className="sr-only">Volume</span>
          <input
            type="range"
            min={0}
            max={100}
            step={1}
            value={volume ?? s.volume}
            disabled={busy}
            onChange={(e) => setVolume(Number(e.target.value))}
            onPointerUp={commitVolume}
            onKeyUp={commitVolume}
            className="w-full accent-primary disabled:opacity-60"
          />
        </label>
      )}
    </Card>
  )
}

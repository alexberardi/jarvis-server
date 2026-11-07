import { useState } from 'react'
import { type NowPlaying, type Send, position, trackKey } from './nowPlayingModel'
import { Heart, Home, Music, Pause, Play, Repeat, Repeat1, Shuffle, SkipBack, SkipForward, Volume2 } from 'lucide-react'

const clock = (s: number) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, '0')}`

function Cover({ n }: { n: NowPlaying }) {
  const key = trackKey(n)
  const [failed, setFailed] = useState('')
  if (failed === key) return <div className="np-cover np-cover-blank" aria-hidden="true"><Music size={64} /></div>
  return <img className="np-cover" src={`/now-playing/cover?v=${encodeURIComponent(key)}`} alt={n.state.media_album ? `Cover of ${n.state.media_album}` : 'Cover'} onError={() => setFailed(key)} />
}

// A range sent once, when it is let go, not on every step of the drag.
function Slider({ label, value, max, onCommit }: { label: string; value: number; max: number; onCommit: (v: number) => void }) {
  const [dragging, setDragging] = useState<number | null>(null)
  const commit = () => { if (dragging !== null) { onCommit(dragging); setDragging(null) } }
  return <input type="range" aria-label={label} min={0} max={max} step={1} value={dragging ?? value}
    onChange={e => setDragging(Number(e.target.value))} onPointerUp={commit} onKeyUp={commit} onBlur={commit} />
}

export function NowPlayingView({ n, now, send, onHome }: { n: NowPlaying; now: Date; send: Send; onHome: () => void }) {
  const s = n.state
  const can = (a: string) => s.actions.includes(a)
  const at = position(n, now)
  const playing = s.state === 'playing'
  const RepeatIcon = s.repeat === 'one' ? Repeat1 : Repeat
  const repeatLabel = s.repeat === 'one' ? 'Repeat one' : s.repeat === 'all' ? 'Repeat all' : 'Repeat off'
  return <section className="now-playing" aria-label="Now playing">
    <div className="np-top">
      <p className="np-from">{s.context ? <>Playing from <strong>{s.context}</strong></> : <>Playing on <strong>{n.name}</strong></>}</p>
      <button onClick={onHome}><Home size={22} aria-hidden="true" />Back to home</button>
    </div>
    <div className="np-body">
      <Cover n={n} />
      <div className="np-main">
        <h1>{s.media_title || 'Unknown track'}</h1>
        {s.media_artist && <p className="np-artist">{s.media_artist}</p>}
        {s.context && <p className="display-note">{n.name}</p>}
        {s.duration ? <div className="np-seek">
          {can('seek') ? <Slider label="Position" value={Math.round(at)} max={Math.round(s.duration)} onCommit={v => send({ action: 'seek', position: v })} />
            : <progress aria-label="Position" max={s.duration} value={at} />}
          <div className="np-times"><span>{clock(at)}</span><span>{clock(s.duration)}</span></div>
        </div> : null}
        <div className="np-controls">
          {s.shuffle !== undefined && can('shuffle') && <button className="np-toggle" aria-label="Shuffle" aria-pressed={s.shuffle} onClick={() => send({ action: 'shuffle' })}><Shuffle size={26} aria-hidden="true" /></button>}
          {can('previous') && <button aria-label="Previous track" onClick={() => send({ action: 'previous' })}><SkipBack size={30} aria-hidden="true" /></button>}
          {(playing ? can('pause') : can('play')) && <button className="np-play" aria-label={playing ? 'Pause' : 'Play'} onClick={() => send({ action: playing ? 'pause' : 'play' })}>{playing ? <Pause size={40} aria-hidden="true" /> : <Play size={40} aria-hidden="true" />}</button>}
          {can('next') && <button aria-label="Next track" onClick={() => send({ action: 'next' })}><SkipForward size={30} aria-hidden="true" /></button>}
          {s.repeat !== undefined && can('repeat') && <button className="np-toggle" aria-label={repeatLabel} aria-pressed={s.repeat !== 'off'} onClick={() => send({ action: 'repeat' })}><RepeatIcon size={26} aria-hidden="true" /></button>}
        </div>
        <div className="np-extra">
          {s.volume !== undefined && can('volume') && <label className="np-volume"><Volume2 size={24} aria-hidden="true" /><Slider label="Volume" value={s.volume} max={100} onCommit={v => send({ action: 'volume', volume: v })} /></label>}
          {s.liked !== undefined && can('like') && <button className="np-toggle" aria-label={s.liked ? 'Remove from liked' : 'Like this track'} aria-pressed={s.liked} onClick={() => send({ action: 'like' })}><Heart size={26} fill={s.liked ? 'currentColor' : 'none'} aria-hidden="true" /></button>}
        </div>
      </div>
    </div>
  </section>
}

// NowPlayingBar brings the view back on the other screens after Home.
export function NowPlayingBar({ n, onOpen }: { n: NowPlaying; onOpen: () => void }) {
  return <button className="np-bar" onClick={onOpen}>
    <Music size={20} aria-hidden="true" />
    <span className="np-bar-text">{n.state.state === 'paused' ? 'Paused' : 'Now playing'}: <strong>{n.state.media_title || n.name}</strong>{n.state.media_artist && ` · ${n.state.media_artist}`}</span>
    <span className="np-bar-open">Open</span>
  </button>
}

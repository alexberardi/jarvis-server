import { useEffect, useMemo, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { Home, Lightbulb, Play, Shield, Star, RotateCcw } from 'lucide-react'
import './Display.css'
import SpeechWaveform from './SpeechWaveform'

type Config = { waveform_colour?: string; waveform_emphasis?: number; enabled: boolean; clock: string; hour_24: boolean; timezone: string; idle_seconds: number; slide_seconds: number; photos: string[]; favourites: string[]; night_dim: boolean; dim_start: number; dim_end: number; dim_percent: number; shortcuts: { name: string; url: string }[]; destination: string }
type Device = { id: string; name: string; domain: string; entity_id: string; room_name: string; supported_actions: { button_action: string; button_text: string }[] | null }
type State = { configuration: Config; voice: string; last_interaction: number; playback: { active: boolean; kind: string; controllable: boolean; paused?: boolean; position?: number; duration?: number }; theme_url: string; receivers: Record<string, string>; status: string }
async function CallDisplay(path: string, body?: unknown) {
  const r = await fetch(`/api/${path}`, { method: body === undefined ? 'GET' : 'POST', headers: { 'X-Jarvis-Display': '1', 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) })
  const raw = await r.text()
  let value: Record<string, unknown>
  try { value = JSON.parse(raw) } catch { throw new Error(raw || 'Jarvis is unavailable') }
  if (!r.ok || value.success === false || value.error) throw new Error(String(value.error || 'Jarvis could not complete this request'))
  return value
}
export function ShowClock({ now, config }: { now: Date; config: Config }) {
  const options: Intl.DateTimeFormatOptions = { ...(config.timezone ? { timeZone: config.timezone } : {}), hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: !config.hour_24 }
  const parts = new Intl.DateTimeFormat('en-AU', { ...options, hour12: false }).formatToParts(now)
  const get = (type: string) => Number(parts.find(p => p.type === type)?.value || 0)
  const hour = get('hour'), minute = get('minute'), second = get('second')
  return <div className="display-clock" data-clock={config.clock} data-hour-format={config.hour_24 ? '24' : '12'}>
    {(config.clock === 'analogue' || config.clock === 'combined') && <svg className="analogue" viewBox="0 0 200 200" role="img" aria-label={now.toLocaleTimeString('en-AU', options)}>
      <circle cx="100" cy="100" r="94" fill="none" stroke="currentColor" strokeWidth="2" />
      {Array.from({ length: 12 }, (_, i) => <line key={i} x1="100" y1="10" x2="100" y2="20" stroke="currentColor" strokeWidth="3" transform={`rotate(${i * 30} 100 100)`} />)}
      <line x1="100" y1="100" x2="100" y2="48" stroke="currentColor" strokeWidth="6" transform={`rotate(${hour * 30 + minute / 2} 100 100)`} />
      <line x1="100" y1="100" x2="100" y2="25" stroke="currentColor" strokeWidth="4" transform={`rotate(${minute * 6 + second / 10} 100 100)`} />
      <circle cx="100" cy="100" r="4" fill="currentColor" />
    </svg>}
    {config.clock !== 'analogue' && config.clock !== 'photos' && <time className="digital">{now.toLocaleTimeString('en-AU', { ...options, second: undefined })}</time>}
    <p>{now.toLocaleDateString('en-AU', { ...(config.timezone ? { timeZone: config.timezone } : {}), weekday: 'long', day: 'numeric', month: 'long' })}</p>
  </div>
}
export function ShowDisplay() {
  const [state, setState] = useState<State | null>(null)
  const [devices, setDevices] = useState<Device[]>([])
  const [parameters, setParameters] = useState<{ device: Device; action: string; request?: string } | null>(null)
  const [parameterValue, setParameterValue] = useState('')
  const [hints, setHints] = useState<Record<string, { min_value?: number; max_value?: number; step?: number; features?: string[]; unit?: string }>>({})
  const parameterDialog = useRef<HTMLDialogElement>(null)
  const [states, setStates] = useState<Record<string, string>>({})
  const [view, setView] = useState('Home')
  const [now, setNow] = useState(() => new Date())
  const [lastTouch, setLastTouch] = useState(() => Date.now())
  const [error, setError] = useState('')
  const [offline, setOffline] = useState(false)
  const [search, setSearch] = useState('')
  const [room, setRoom] = useState('')
  const [page, setPage] = useState(0)
  const [busy, setBusy] = useState('')
  const [url, setURL] = useState('')
  const [destination, setDestination] = useState('')
  const [exit, setExit] = useState(false)
  const [pin, setPIN] = useState('')
  const [themeReady, setThemeReady] = useState(false)
  const [themeFailed, setThemeFailed] = useState(false)
  const [confirmation, setConfirmation] = useState<{ device: Device; action: string; request?: string } | null>(null)
  const frame = useRef<HTMLIFrameElement>(null)
  const dialog = useRef<HTMLDialogElement>(null)
  const confirmDialog = useRef<HTMLDialogElement>(null)
  const config = state?.configuration
  useEffect(() => {
    let active = true, pending = false, previousTheme = ''
    const poll = async () => {
      if (pending) return
      pending = true
      try { const s = await CallDisplay('state') as unknown as State; if (active) { if (previousTheme !== s.theme_url) { previousTheme = s.theme_url; setThemeReady(false); setThemeFailed(false) } setState(s) } } catch (e) { if (active) setError((e as Error).message) } finally { pending = false }
    }
    void poll(); const t = setInterval(() => { setNow(new Date()); void poll() }, 1000)
    return () => { active = false; clearInterval(t) }
  }, [])
  useEffect(() => {
    let active = true
    const load = async () => { try { const d = await CallDisplay('devices'); if (active) { setDevices(d as unknown as Device[]); setOffline(false) } } catch { if (active) setOffline(true) } }
    void load(); const t = setInterval(() => void load(), 30000); return () => { active = false; clearInterval(t) }
  }, [])
  useEffect(() => { if (themeReady || !state?.theme_url) return; const t = setTimeout(() => setThemeFailed(true), 5000); return () => clearTimeout(t) }, [themeReady, state?.theme_url])
  useEffect(() => { if (exit) dialog.current?.showModal(); else dialog.current?.close() }, [exit])
  useEffect(() => { if (parameters) parameterDialog.current?.showModal(); else parameterDialog.current?.close() }, [parameters])
  useEffect(() => { if (confirmation) confirmDialog.current?.showModal(); else confirmDialog.current?.close() }, [confirmation])
  const filtered = useMemo(() => devices.filter(d => (view !== 'Home' || config?.favourites.includes(d.id)) && (!room || d.room_name === room) && `${d.name} ${d.room_name}`.toLowerCase().includes(search.toLowerCase())), [devices, view, config?.favourites, room, search])
  const visible = filtered.slice(page * 24, (page + 1) * 24)
  const visibleKey = visible.map(d => d.id).join(',')
  useEffect(() => {
    if (!visibleKey) return
    let cancelled = false
    const load = async () => {
      const ids = visibleKey.split(',')
      // Four workers bound requests for large catalogues and slow plugin nodes.
      await Promise.all(Array.from({ length: Math.min(4, ids.length) }, async () => {
        while (ids.length && !cancelled) { const id = ids.shift()!; try { const s = await CallDisplay(`devices/${encodeURIComponent(id)}/state`); const value = s.state as Record<string, unknown> | undefined; if (!cancelled && value) { setStates(old => ({ ...old, [id]: String(value.state ?? 'Unknown') })); if (s.ui_hints) setHints(old => ({ ...old, [id]: s.ui_hints as typeof old[string] })) } } catch { /* Keep the last confirmed state. */ } }
      }))
    }
    void load(); const t = setInterval(() => void load(), 15000); return () => { cancelled = true; clearInterval(t) }
  }, [visibleKey])
  const respond = (request: string | undefined, result: unknown) => { if (request) frame.current?.contentWindow?.postMessage({ api_version: 1, type: 'result', request_id: request, result }, '*') }
  const control = async (device: Device, action: string, confirmed = false, request?: string, data?: Record<string, unknown>) => {
    if (!device.supported_actions?.some(a => a.button_action === action)) { respond(request, { error: 'Unsupported action' }); return }
    if ((action === 'unlock' || action.startsWith('open')) && !confirmed) { setConfirmation({ device, action, request }); return }
    if (['set_temperature', 'set_mode', 'set_hvac_mode', 'set_brightness', 'set_percentage'].includes(action) && !data) { setParameters({ device, action, request }); setParameterValue(''); return }
    setBusy(device.id); setError('')
    try {
      await CallDisplay(`devices/${encodeURIComponent(device.id)}/control`, { action, confirmed, data })
      const s = await CallDisplay(`devices/${encodeURIComponent(device.id)}/state`)
      const value = s.state as Record<string, unknown> | undefined
      if (value) setStates(old => ({ ...old, [device.id]: String(value.state ?? 'Unknown') }))
      respond(request, { success: true })
    } catch (e) { setError((e as Error).message); respond(request, { error: (e as Error).message }) } finally { setBusy('') }
  }
  const media = async (action: string, target = url, local?: string, query?: string) => {
    setError('')
    try { await CallDisplay('media', { action, url: target, destination: local || destination || config?.destination, query }) } catch (e) { setError((e as Error).message) }
  }
  useEffect(() => {
    const receive = (event: MessageEvent) => {
      if (event.source !== frame.current?.contentWindow || event.origin !== 'null') return
      const m = event.data
      if (!m || m.api_version !== 1 || typeof m.type !== 'string') return
      if (m.type === 'activity') { setLastTouch(Date.now()); return }
      if (m.type === 'device.action' || m.type === 'media.control') setLastTouch(Date.now())
      if (m.type === 'ready') { setThemeReady(true); return }
      if (m.type === 'device.action' && typeof m.device_id === 'string' && typeof m.action === 'string') { const d = devices.find(d => d.id === m.device_id); if (d) void control(d, m.action, false, String(m.request_id || ''), m.data && typeof m.data === 'object' ? m.data : undefined); return }
      if (m.type === 'media.control' && ['play', 'pause', 'resume', 'seek', 'stop', 'status'].includes(m.action) && JSON.stringify(m).length < 8192) {
        void CallDisplay('media', { action: m.action, url: typeof m.url === 'string' ? m.url : '', query: typeof m.query === 'string' ? m.query : '', seconds: Number(m.seconds || 0), destination: typeof m.destination === 'string' ? m.destination : config?.destination }).then(v => respond(String(m.request_id || ''), v)).catch(e => { setError(e.message); respond(String(m.request_id || ''), { error: e.message }) })
      }
    }
    window.addEventListener('message', receive); return () => window.removeEventListener('message', receive)
  })
  useEffect(() => { if (themeReady) frame.current?.contentWindow?.postMessage({ api_version: 1, type: 'state', devices: devices.map(d => ({ ...d, state: states[d.id] })), clock: { timestamp: now.getTime(), timezone: config?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone }, display: { voice: state?.voice, view }, media: state?.playback }, '*') }, [themeReady, devices, states, now, config?.timezone, state?.voice, state?.playback, view])
  if (!state || !config) return <main className="display-shell"><h1>Jarvis</h1><p role="status">Loading the display…</p>{error && <p role="alert">{error}</p>}</main>
  const idle = !exit && !confirmation && !parameters && !state.playback.active && now.getTime() - Math.max(lastTouch, state.last_interaction) >= config.idle_seconds * 1000
  const hour = Number(new Intl.DateTimeFormat('en-AU', { ...(config.timezone ? { timeZone: config.timezone } : {}), hour: '2-digit', hour12: false }).format(now)) % 24
  const dim = config.night_dim && (config.dim_start < config.dim_end ? hour >= config.dim_start && hour < config.dim_end : hour >= config.dim_start || hour < config.dim_end)
  const photo = config.photos.length ? config.photos[Math.floor(now.getTime() / (config.slide_seconds * 1000)) % config.photos.length] : ''
  const custom = state.theme_url && !themeFailed
  return <main className="display-shell" onPointerDown={() => setLastTouch(Date.now())} onKeyDown={() => setLastTouch(Date.now())} style={{ filter: dim ? `brightness(${config.dim_percent}%)` : undefined }}>
    <SpeechWaveform voice={state.voice} theme={frame} colour={config.waveform_colour || '#a56bff'} emphasis={config.waveform_emphasis || 150} />
    <header><span className="brand">Jarvis</span><span role="status" className="voice-state">{state.voice === 'idle' ? 'Ready when you are' : state.voice.replace('_', ' ')}</span><button onClick={() => setExit(true)}><Shield size={21} aria-hidden="true" />Exit</button></header>
    {error && <div className="display-error" role="alert">{error}<button onClick={() => setError('')}>Dismiss</button></div>}
    {offline && <p className="display-note" role="status">Server offline. Clocks and cached themes remain available.</p>}
    {state.status && <p role="status">{state.status}</p>}
    {!config.enabled && <p role="status">Display mode is disabled. Set an exit PIN and enable this node in server settings.</p>}
    {idle ? <button className="idle-screen" aria-label="Wake Jarvis display" onClick={() => setLastTouch(Date.now())}>{photo && <img src={`/photo/${photo}`} alt="Slideshow photo" />}<ShowClock now={now} config={config} /></button> : <>
      <nav aria-label="Display views">{[{ name: 'Home', icon: Home }, { name: 'Devices', icon: Lightbulb }, { name: 'Media', icon: Play }].map(({ name, icon: Icon }) => <button key={name} aria-current={view === name ? 'page' : undefined} onClick={() => { setView(name); setPage(0) }}><Icon size={22} aria-hidden="true" />{name}</button>)}<button onClick={() => { setThemeFailed(true); void CallDisplay('recover', {}) }}><RotateCcw size={21} aria-hidden="true" />Built-in view</button></nav>
      {custom && <iframe ref={frame} title="Jarvis theme" className={`theme-frame ${themeReady ? '' : 'theme-loading'}`} sandbox="allow-scripts" src={state.theme_url} onError={() => setThemeFailed(true)} />}
      {(!custom || !themeReady) && <section className="display-content" aria-label={view}>
        {view === 'Home' && <ShowClock now={now} config={config} />}
        {view !== 'Media' && <>
          <div className="device-filters"><label>Search devices<input value={search} onChange={e => { setSearch(e.target.value); setPage(0) }} /></label><label>Room<select value={room} onChange={e => { setRoom(e.target.value); setPage(0) }}><option value="">All rooms</option>{[...new Set(devices.map(d => d.room_name).filter(Boolean))].sort().map(r => <option key={r}>{r}</option>)}</select></label></div>
          {!filtered.length && <p>{view === 'Home' ? 'Choose favourites in server settings, or browse Devices.' : 'No matching devices. Discover devices through Jarvis plugins or Home Assistant.'}</p>}
          <div className="device-grid">{visible.map(d => <article key={d.id}><div className="device-heading"><h2>{d.name}</h2>{config.favourites.includes(d.id) && <Star size={18} aria-label="Favourite" />}</div><p>{d.room_name || 'No room'} · <span>{states[d.id] || 'State unavailable'}</span></p><div className="device-actions">{d.supported_actions?.map(a => <button key={a.button_action} disabled={busy === d.id || offline} onClick={() => void control(d, a.button_action)}>{a.button_text}</button>)}</div></article>)}</div>
          {filtered.length > 24 && <div className="pagination"><button disabled={!page} onClick={() => setPage(p => p - 1)}>Previous</button><span>{page + 1} / {Math.ceil(filtered.length / 24)}</span><button disabled={(page + 1) * 24 >= filtered.length} onClick={() => setPage(p => p + 1)}>Next</button></div>}
        </>}
        {view === 'Media' && <><h1>Play something</h1><form onSubmit={e => { e.preventDefault(); void media('play') }}><label>Video link or streaming shortcut<input value={url} onChange={e => setURL(e.target.value)} /></label><label>Playback destination<select value={destination || config.destination} onChange={e => setDestination(e.target.value)}><option value="local">This display</option>{devices.filter(d => d.domain === 'media_player').map(d => <option key={d.id} value={d.name}>{d.name}</option>)}</select></label><div className="media-buttons"><button type="submit">Play</button><button type="button" onClick={() => void media('play', '', undefined, url)}>Search YouTube</button><button type="button" onClick={() => { setDestination('local'); void media('play', url, 'local') }}>Play locally</button></div></form>
          <div className="media-buttons">{config.shortcuts.map(s => <button key={s.name} onClick={() => void media('play', s.name, 'local')}>{s.name}</button>)}</div>
          <p>{state.playback.active ? `Playing ${state.playback.kind || 'received media'}` : 'Nothing playing'}</p><div className="media-buttons"><button disabled={!state.playback.controllable} onClick={() => void media('pause')}>Pause</button><button disabled={!state.playback.controllable} onClick={() => void media('resume')}>Resume</button><button onClick={() => void media('stop')}>Stop and return</button></div>
          {state.playback.controllable && <label>Seek<input type="range" min="0" max={state.playback.duration || 0} value={state.playback.position || 0} onChange={e => { void CallDisplay('media', { action: 'seek', seconds: Number(e.target.value), destination: 'local' }).catch(err => setError(err.message)) }} /></label>}
          <h2>Receivers</h2>{Object.entries(state.receivers).map(([k, v]) => <p key={k}>{k === 'cast' ? 'Experimental Cast V2' : 'Playercast (compatible Playercast senders)'}: {v}</p>)}<p className="display-note">Streaming services use this device’s browser sign-in. Control their pages by touch.</p></>}
      </section>}
    </>}
    <dialog ref={parameterDialog} onCancel={() => setParameters(null)}><form onSubmit={e => {
      e.preventDefault(); if (!parameters) return
      const key = parameters.action.includes('temperature') ? 'temperature' : parameters.action.includes('mode') ? 'mode' : parameters.action.includes('brightness') ? 'brightness' : 'percentage'
      void control(parameters.device, parameters.action, false, parameters.request, { [key]: key === 'mode' ? parameterValue : Number(parameterValue) }); setParameters(null)
    }}><h2>{parameters?.device.name}</h2><label>{parameters?.action.replaceAll('_', ' ')} {parameters && hints[parameters.device.id]?.unit}
      {parameters?.action.includes('mode') ? <input autoFocus required maxLength={50} value={parameterValue} list="action-modes" onChange={e => setParameterValue(e.target.value)} /> : <input autoFocus required type="number" step={parameters && hints[parameters.device.id]?.step || 1} min={(parameters && hints[parameters.device.id]?.min_value) ?? 0} max={(parameters && hints[parameters.device.id]?.max_value) ?? 100} value={parameterValue} onChange={e => setParameterValue(e.target.value)} />}
    </label><datalist id="action-modes">{parameters && hints[parameters.device.id]?.features?.map(v => <option key={v} value={v} />)}</datalist><div className="media-buttons"><button type="submit">Apply</button><button type="button" onClick={() => setParameters(null)}>Cancel</button></div></form></dialog>
    <dialog ref={dialog} onCancel={() => setExit(false)}><form onSubmit={async e => { e.preventDefault(); try { await CallDisplay('exit', { pin }); setPIN(''); setExit(false) } catch (e) { setError((e as Error).message); setPIN('') } }}><h2>Return to the desktop</h2><p>Jarvis voice continues running.</p><label>Administrator PIN<input autoFocus type="password" inputMode="numeric" pattern="[0-9]{4,20}" minLength={4} maxLength={20} value={pin} onChange={e => setPIN(e.target.value)} autoComplete="off" /></label><div className="media-buttons"><button type="submit">Exit display</button><button type="button" onClick={() => { setPIN(''); setExit(false) }}>Cancel</button></div></form></dialog>
    <dialog ref={confirmDialog} onCancel={() => setConfirmation(null)}><h2>Confirm access</h2><p>{confirmation?.action.replaceAll('_', ' ')}: {confirmation?.device.name}?</p><div className="media-buttons"><button onClick={() => { if (confirmation) void control(confirmation.device, confirmation.action, true, confirmation.request); setConfirmation(null) }}>Confirm</button><button onClick={() => { respond(confirmation?.request, { error: 'Cancelled' }); setConfirmation(null) }}>Cancel</button></div></dialog>
  </main>
}
if (document.getElementById('root')) createRoot(document.getElementById('root')!).render(<ShowDisplay />)

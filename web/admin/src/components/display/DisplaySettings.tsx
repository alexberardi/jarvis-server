import { useEffect, useState } from 'react'
import { apiClient } from '@/api/client'
import { Button, Input, Select, Card } from '@/components/ui'
import { errorMessage } from '@/lib/errors'

type Value = string | number | boolean | string[] | { name: string; url: string }[]
type Settings = { devices: { id: string; name: string; room: string }[]; overrides: Record<string, Value>; configuration: Record<string, Value>; inherited: Record<string, Value>; revision: string; applied_revision: string; apply_error: string; pin_configured: boolean; pin_overridden: boolean }
type Asset = { id: string; kind: string; name: string }
const prefix = '/api/cc/api/v0/display'
const fields = [
  ['enabled', 'Dedicated display mode', 'boolean'], ['theme', 'Theme', 'theme'],
  ['waveform_colour', 'Speech strand colour', 'colour'], ['waveform_emphasis', 'Speech waveform emphasis (%)', 'number'],
  ['favourites', 'Favourite device IDs (comma separated)', 'list'],
  ['idle_seconds', 'Idle timeout (seconds)', 'number'], ['slide_seconds', 'Slideshow interval (seconds)', 'number'],
  ['photos', 'Photo IDs (comma separated)', 'list'], ['clock', 'Idle view', 'clock'], ['hour_24', '24-hour clock', 'boolean'],
  ['timezone', 'Timezone (blank uses the node timezone)', 'text'], ['night_dim', 'Night dimming', 'boolean'],
  ['dim_start', 'Dimming starts (hour, 0 to 23)', 'number'], ['dim_end', 'Dimming ends (hour, 0 to 23)', 'number'], ['dim_percent', 'Night brightness (%)', 'number'],
  ['destination', 'Default playback destination (local or device name)', 'text'],
  ['shortcuts', 'Streaming shortcuts (one Name | https://URL per line)', 'shortcuts'],
  ['playercast', 'Playercast receiver (Playercast senders)', 'boolean'], ['experimental_cast', 'Experimental Cast V2 receiver', 'boolean'], ['receiver_name', 'Receiver name', 'text'],
] as const
export default function DisplaySettings({ scope, label }: { scope: string; label: string }) {
  const [opened, setOpened] = useState(false)
  const [settings, setSettings] = useState<Settings | null>(null)
  const [texts, setTexts] = useState<Record<string, string>>({})
  const [draft, setDraft] = useState<Record<string, Value>>({})
  const [assets, setAssets] = useState<Asset[]>([])
  const [pin, setPin] = useState('')
  const [inheritPin, setInheritPin] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [preview, setPreview] = useState('')
  useEffect(() => {
    if (!opened) return
    let active = true
    const load = async () => {
      const [s, a] = await Promise.all([apiClient.get<Settings>(`${prefix}/config/${encodeURIComponent(scope)}`), apiClient.get<Asset[]>(`${prefix}/assets`)])
      if (active) { setSettings(s.data); setDraft(s.data.overrides); setTexts({}); setAssets(a.data) }
    }
    void load().catch(e => { if (active) setError(errorMessage(e, 'Could not load display settings')) })
    return () => { active = false }
  }, [opened, scope])
  const refresh = async () => { const { data } = await apiClient.get<Settings>(`${prefix}/config/${encodeURIComponent(scope)}`); setSettings(data) }
  const update = (key: string, value: Value) => setDraft(old => ({ ...old, [key]: value }))
  const save = async () => {
    setSaving(true); setError(''); setMessage('')
    try {
      if (pin && !/^[0-9]{4,20}$/.test(pin)) throw new Error('Use a numeric PIN with 4 to 20 digits')
      const { data } = await apiClient.put<Settings>(`${prefix}/config/${encodeURIComponent(scope)}`, { overrides: draft, ...(pin ? { pin } : inheritPin ? { pin: '' } : {}) })
      setSettings(data); setDraft(data.overrides); setTexts({}); setPin(''); setInheritPin(false); setMessage('Saved. Nodes apply settings on reconnect or within 30 seconds.')
    } catch (e) { setError(errorMessage(e, 'Could not save display settings')) } finally { setSaving(false) }
  }
  const upload = async (file: File, kind: string) => {
    setSaving(true); setError('')
    try {
      const { data } = await apiClient.post<Asset>(`${prefix}/assets?kind=${kind}&name=${encodeURIComponent(file.name)}`, file, { headers: { 'Content-Type': 'application/octet-stream' } })
      setAssets(old => [...old.filter(a => a.id !== data.id), data])
      if (kind === 'theme') update('theme', data.id)
      else { const current = (draft.photos ?? settings?.configuration.photos ?? []) as string[]; update('photos', [...new Set([...current, data.id])]) }
      setMessage('Uploaded. Save settings to assign this asset.')
    } catch (e) { setError(errorMessage(e, 'Could not upload asset')) } finally { setSaving(false) }
  }
  const exportAsset = async (asset: Asset) => {
    try { const { data } = await apiClient.get(`${prefix}/assets/${asset.id}`, { responseType: 'blob' }); const url = URL.createObjectURL(data); const a = document.createElement('a'); a.href = url; a.download = asset.name || `${asset.id}.${asset.kind === 'theme' ? 'zip' : 'png'}`; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000) } catch (e) { setError(errorMessage(e, 'Could not export asset')) }
  }
  const previewAsset = async (asset: Asset) => {
    try { const { data } = await apiClient.get<{ html: string }>(`${prefix}/assets/${asset.id}?preview=1`); setPreview(data.html) } catch (e) { setError(errorMessage(e, 'Could not preview theme')) }
  }
  return <details className="my-4" onToggle={e => setOpened(e.currentTarget.open)}><summary className="min-h-11 cursor-pointer py-3 font-medium text-text">{label}</summary>
    {opened && <Card className="mt-2 space-y-5">
      {error && <p role="alert" className="text-danger">{error}</p>}{message && <p role="status" className="text-text-muted">{message}</p>}
      {!settings ? <p role="status">Loading display settings…</p> : <>
        <p className="text-small text-text-muted">{scope === 'defaults' ? 'Defaults apply to display-capable nodes. Existing voice nodes remain unchanged.' : 'Each field inherits the server default until overridden.'}</p>
        {scope !== 'defaults' && <div className="space-y-2"><p role="status">{settings.apply_error ? `Application failed: ${settings.apply_error}` : settings.revision === settings.applied_revision ? 'Saved settings are applied.' : 'Saved settings are waiting for the node.'}</p><p className="break-all text-small text-text-muted">Saved revision: {settings.revision}<br />Applied revision: {settings.applied_revision || 'Not yet reported'}</p><Button size="sm" variant="secondary" onClick={() => void refresh().catch(e => setError(errorMessage(e, 'Could not refresh status')))}>Refresh applied status</Button></div>}
        {scope !== 'defaults' && settings.devices?.length > 0 && <fieldset className="grid max-h-64 gap-2 overflow-y-auto rounded-lg border border-border p-3"><legend className="px-2">Choose favourites</legend>{settings.devices.map(d => <label key={d.id} className="flex min-h-11 items-center gap-3"><input type="checkbox" checked={((draft.favourites ?? settings.inherited.favourites) as string[]).includes(d.id)} onChange={e => { const current = (draft.favourites ?? settings.inherited.favourites) as string[]; update('favourites', e.target.checked ? [...current, d.id] : current.filter(id => id !== d.id)); setTexts(old => { const next = { ...old }; delete next.favourites; return next }) }} />{d.name}{d.room ? ` (${d.room})` : ''}</label>)}</fieldset>}
        <div className="grid gap-5 md:grid-cols-2">{fields.map(([key, title, type]) => {
          const overridden = Object.hasOwn(draft, key)
          const value = draft[key] ?? settings.inherited?.[key] ?? settings.configuration[key]
          return <div key={key} className="min-w-0 space-y-2">
            {type === 'boolean' ? <label className="flex min-h-11 items-center gap-3 text-text"><input type="checkbox" checked={Boolean(value)} onChange={e => update(key, e.target.checked)} />{title}</label> : type === 'clock' || type === 'theme' ? <Select label={title} value={String(value)} onValueChange={v => update(key, v)} options={type === 'clock' ? ['digital', 'analogue', 'combined', 'photos'].map(v => ({ value: v, label: v[0].toUpperCase() + v.slice(1) })) : [{ value: 'builtin', label: 'Built-in' }, ...assets.filter(a => a.kind === 'theme').map(a => ({ value: a.id, label: a.name }))]} /> : type === 'shortcuts' ? <label className="grid gap-2 text-small text-text">{title}<textarea rows={3} className="w-full rounded-lg border border-border bg-surface p-3 text-text" value={texts[key] ?? (value as { name: string; url: string }[]).map(s => `${s.name} | ${s.url}`).join('\n')} onChange={e => { setTexts(old => ({ ...old, [key]: e.target.value })); update(key, e.target.value.split('\n').filter(Boolean).map(line => { const [name, ...url] = line.split('|'); return { name: name.trim(), url: url.join('|').trim() } })) }} /></label> : <Input label={title} type={type === 'number' ? 'number' : type === 'colour' ? 'color' : 'text'} value={type === 'list' ? texts[key] ?? (value as string[]).join(', ') : String(value ?? '')} onChange={e => { if (type === 'list') setTexts(old => ({ ...old, [key]: e.target.value })); update(key, type === 'number' ? Number(e.target.value) : type === 'list' ? e.target.value.split(',').map(s => s.trim()).filter(Boolean) : e.target.value) }} />}
            {scope !== 'defaults' && <Button size="sm" variant="ghost" disabled={!overridden} onClick={() => { setTexts(old => { const next = { ...old }; delete next[key]; return next }); setDraft(old => { const next = { ...old }; delete next[key]; return next }) }}>{overridden ? 'Use server default' : 'Using server default'}</Button>}
          </div>
        })}</div>
        <div className="grid gap-5 md:grid-cols-2"><Input label="Exit PIN" type="password" inputMode="numeric" autoComplete="new-password" minLength={4} maxLength={20} pattern="[0-9]{4,20}" value={pin} onChange={e => { setPin(e.target.value); setInheritPin(false) }} hint={`${settings.pin_configured ? 'A PIN is configured. ' : ''}Use 4 to 20 digits. Leave blank to retain it. The PIN is never shown again.`} />{scope !== 'defaults' && <label className="flex min-h-11 items-center gap-3"><input type="checkbox" checked={inheritPin} onChange={e => { setInheritPin(e.target.checked); setPin('') }} />Use server exit PIN</label>}</div>
        <p className="text-small text-text-muted">Experimental Cast has limited sender compatibility. Playercast requires compatible Playercast senders. Receiver failures leave voice and the display running.</p>
        <Button loading={saving} onClick={() => void save()}>Save display settings</Button>
        <section className="space-y-3" aria-label="Display assets"><h3 className="text-subheading">Themes and photos</h3><div className="grid gap-4 md:grid-cols-2"><Input label="Import theme ZIP (API version 1, maximum 20 MiB)" type="file" accept=".zip" disabled={saving} onChange={e => { const f = e.target.files?.[0]; if (f) void upload(f, 'theme'); e.target.value = '' }} /><Input label="Upload slideshow photo (PNG or JPEG)" type="file" accept="image/png,image/jpeg" disabled={saving} onChange={e => { const f = e.target.files?.[0]; if (f) void upload(f, 'photo'); e.target.value = '' }} /></div>
          {assets.map(a => <div key={a.id} className="flex flex-wrap items-center gap-2 border-t border-border py-3"><span className="min-w-0 flex-1 break-all">{a.name} <small className="text-text-muted">{a.kind}<br />{a.id}</small></span><Button size="sm" variant="secondary" onClick={() => void exportAsset(a)}>Export</Button>{a.kind === 'theme' && <Button size="sm" variant="secondary" onClick={() => void previewAsset(a)}>Preview</Button>}<Button size="sm" variant="ghost" onClick={async () => { if (!window.confirm(`Remove ${a.name}?`)) return; try { await apiClient.delete(`${prefix}/assets/${a.id}`); setAssets(old => old.filter(v => v.id !== a.id)) } catch (e) { setError(errorMessage(e, 'Could not remove asset')) } }}>Remove</Button></div>)}
          {preview && <><Button size="sm" variant="secondary" onClick={() => setPreview('')}>Close preview</Button><iframe title="Theme preview" sandbox="allow-scripts" srcDoc={preview} onLoad={e => e.currentTarget.contentWindow?.postMessage({ api_version: 1, type: 'state', devices: [{ id: 'preview-light', name: 'Preview light', room_name: 'Living room', state: 'on', supported_actions: [{ button_action: 'turn_off', button_text: 'Turn off' }] }], clock: { timestamp: Date.now(), timezone: Intl.DateTimeFormat().resolvedOptions().timeZone }, display: { voice: 'idle', view: 'Home' }, media: { active: false } }, '*')} className="h-96 w-full rounded-lg border border-border" /></>}
        </section>
      </>}
    </Card>}
  </details>
}

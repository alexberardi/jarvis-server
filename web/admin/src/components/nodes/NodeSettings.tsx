import { useEffect, useState } from 'react'
import { Button, Dialog, Input, Select, SkeletonList, Switch } from '@/components/ui'
import { getNodeConfig, saveNodeConfig, sendBasicNodeConfig, type NodeConfig } from '@/api/nodeConfig'
import { errorMessage, errorStatus } from '@/lib/errors'

interface Props { nodeId: string; name: string; online: boolean; desktop: boolean }
export default function NodeSettings(props: Props) {
  const [open, setOpen] = useState(false)
  return <>
    <Button size="sm" disabled={!props.online} title={props.online ? undefined : 'Connect this node to edit its settings'} onClick={() => setOpen(true)}>Settings</Button>
    {open && <Editor {...props} onClose={() => setOpen(false)} />}
  </>
}

function Editor({ nodeId, name, desktop, onClose }: Props & { onClose: () => void }) {
  const [view, setView] = useState<NodeConfig | null>(null)
  const [draft, setDraft] = useState<Record<string, string | boolean>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [canUseLegacy, setCanUseLegacy] = useState(false)
  const [legacy, setLegacy] = useState(false)
  const [loadKey, setLoadKey] = useState(0)
  const dirty = Object.keys(draft).length > 0

  useEffect(() => {
    let cancelled = false
    getNodeConfig(nodeId).then(v => { if (!cancelled) { setView(v); setError(''); setDraft({}); setLegacy(false); setCanUseLegacy(false) } })
      .catch(e => { if (!cancelled) { setError(errorMessage(e)); setCanUseLegacy(!desktop && errorStatus(e) === 504) } })
    return () => { cancelled = true }
  }, [nodeId, desktop, loadKey])

  function change(key: string, value: string | boolean) {
    setDraft(previous => {
      const next = { ...previous }
      if ((legacy && value === '') || String(value) === String(view?.values[key])) delete next[key]
      else next[key] = value
      return next
    })
    setMessage('')
  }
  async function useLegacy() {
    setBusy(true)
    try { setView(await getNodeConfig(nodeId, true)); setLegacy(true); setError(''); setDraft({}); setCanUseLegacy(false) }
    catch (e) { setError(errorMessage(e)) }
    finally { setBusy(false) }
  }
  async function save(e: React.FormEvent) {
    e.preventDefault()
    if (!view || !dirty) return
    const values: NodeConfig['values'] = {}
    for (const f of view.fields) {
      if (!(f.key in draft)) continue
      const raw = draft[f.key]
      if (raw === '') continue // an untouched manual field has no assumed value
      values[f.key] = f.type === 'number' ? Number(raw) : raw
    }
    if (!Object.keys(values).length) return
    setBusy(true); setError(''); setMessage('')
    try {
      if (legacy) {
        await sendBasicNodeConfig(nodeId, values)
        setMessage('Sent to the node. This version cannot confirm whether the changes were saved.')
      } else {
        setView(await saveNodeConfig(nodeId, values))
        setMessage('Saved on the node. Voice changes take effect between turns or when listening starts.')
      }
      setDraft({})
    } catch (e) { setError(errorMessage(e)) }
    finally { setBusy(false) }
  }
  const groups = [...new Set(view?.fields.map(f => f.group))]
  return <Dialog open onClose={onClose} dismissible={!busy && !dirty} title={`${name} settings`} className="max-w-2xl"
    description="Adjust voice and the controls supported by this node.">
    {legacy && <p className="mb-4 text-small text-text-muted">Current values are unavailable. Leave fields unchanged unless you want to set them. Updates from this node version cannot be confirmed.</p>}
    {error && <p role="alert" className="mb-4 text-small text-danger">{error}</p>}
    {!view && !error && <SkeletonList rows={4} label="Loading node settings" />}
    {canUseLegacy && <Button loading={busy} onClick={useLegacy}>Set basic values without readback</Button>}
    <form onSubmit={save}>
      {groups.map(group => <fieldset key={group} disabled={busy} className="mb-6 min-w-0 space-y-4 border-t border-border pt-4">
        <legend className="pr-3 text-subheading text-text">{group}</legend>
        {view?.fields.filter(f => f.group === group).map(f => {
          const value = draft[f.key] ?? view.values[f.key] ?? ''
          const locked = view.read_only?.includes(f.key) === true
          const hint = locked ? `${f.help ?? ''} Set by a command-line flag; change the agent launch options to edit it.` : f.help
          if (f.type === 'boolean' && !legacy) return <Switch key={f.key} label={f.label} description={hint} checked={Boolean(value)} onCheckedChange={v => change(f.key, v)} disabled={busy || locked} />
          if (f.type === 'boolean') return <Select key={f.key} label={f.label} hint={hint} value={String(value)} options={[{ value: '', label: 'Unchanged' }, { value: 'true', label: 'On' }, { value: 'false', label: 'Off' }]} onChange={e => {
            if (e.target.value === '') setDraft(p => { const n = { ...p }; delete n[f.key]; return n })
            else change(f.key, e.target.value === 'true')
          }} />
          return <Input key={f.key} label={f.label} hint={hint} type={f.type === 'time' ? 'time' : 'number'} value={String(value)} disabled={locked}
            min={f.key in draft ? f.min : undefined} max={f.key in draft ? f.max : undefined} step={f.key in draft ? f.step || 'any' : 'any'}
            required={!legacy || f.key in draft} placeholder={legacy ? 'Unchanged' : undefined} onChange={e => change(f.key, e.target.value)} />
        })}
      </fieldset>)}
      {message && <p role="status" className="mb-4 text-small text-text">{message}</p>}
      <div className="flex flex-wrap gap-2">
        {view && <Button type="submit" variant="primary" loading={busy} disabled={!dirty}>Save changes</Button>}
        <Button type="button" disabled={busy} onClick={() => { setView(null); setDraft({}); setError(''); setMessage(''); setLoadKey(k => k + 1) }}>
          {dirty ? 'Discard changes and reload' : 'Reload'}
        </Button>
        <Button type="button" disabled={busy} variant="ghost" onClick={onClose}>{dirty ? 'Discard and close' : 'Close'}</Button>
      </div>
    </form>
  </Dialog>
}

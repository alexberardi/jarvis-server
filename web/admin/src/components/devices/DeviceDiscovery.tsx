import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { defaultDeviceNode, deviceNodeName, importDevices, requestDeviceJob } from '@/api/devices'
import type { DeviceConfig, DeviceImport, DeviceRoom, DiscoveredDevice } from '@/api/devices'
import { Alert, Badge, Button, Dialog, EmptyState, Input, Select } from '@/components/ui'
import { errorMessage } from '@/lib/errors'

interface Props {
  household: string
  config: DeviceConfig
  rooms: DeviceRoom[]
  roomsError: boolean
  onClose: () => void
  onChanged: () => void
  onExisting: (id: string) => void
  /** The node to scan with first (a Forge test's node), when it is one of config's. */
  nodeId?: string
}

export default function DeviceDiscovery({ household, config, rooms, roomsError, onClose, onChanged, onExisting, nodeId }: Props) {
  const [nodeID, setNodeID] = useState(
    (nodeId && config.nodes.some((n) => n.node_id === nodeId) ? nodeId : defaultDeviceNode(config)?.node_id) ?? '',
  )
  const [results, setResults] = useState<DiscoveredDevice[] | null>(null)
  const [chosen, setChosen] = useState<Record<string, { name: string; room: string }>>({})
  const [busy, setBusy] = useState<'scan' | 'import' | null>(null)
  const [error, setError] = useState('')
  const request = useRef<AbortController | null>(null)
  useEffect(() => () => request.current?.abort(), [])
  const node = config.nodes.find((n) => n.node_id === nodeID)
  const roomOptions = [{ value: '', label: 'Unassigned' }, ...rooms.map((r) => ({ value: r.id, label: r.name }))]
  const count = Object.keys(chosen).length
  const valid = count > 0 && Object.values(chosen).every((d) => d.name.trim())

  const scan = async () => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    setBusy('scan'); setError(''); setResults(null); setChosen({})
    try {
      const result = await requestDeviceJob(nodeID, 'device-scan', controller.signal)
      if (!controller.signal.aborted) setResults(result.devices ?? [])
    } catch (err) {
      if (!controller.signal.aborted) setError(errorMessage(err, 'The scan failed. Check the node and try again.'))
    } finally {
      if (!controller.signal.aborted) setBusy(null)
    }
  }

  const add = async () => {
    const controller = new AbortController()
    request.current = controller
    setBusy('import'); setError('')
    const devices: DeviceImport[] = (results ?? []).filter((d) => chosen[d.entity_id] && !d.already_registered).map((d) => ({
      entity_id: d.entity_id, domain: d.domain, protocol: d.protocol,
      name: chosen[d.entity_id].name.trim(), room_id: chosen[d.entity_id].room || null,
      source: 'direct', manufacturer: d.manufacturer, model: d.model,
      local_ip: d.local_ip, mac_address: d.mac_address, cloud_id: d.cloud_id, device_class: d.device_class,
    }))
    try {
      const result = await importDevices(household, nodeID, devices, controller.signal)
      if (controller.signal.aborted) return
      onChanged()
      toast.success(`Added ${result.created} device${result.created === 1 ? '' : 's'}${result.updated ? `, updated ${result.updated}` : ''}. Open a device to check pairing.`)
      onClose()
    } catch (err) {
      if (controller.signal.aborted) return
      onChanged()
      const status = (err as { response?: { status: number } }).response?.status
      // The server may have committed before the connection failed. Reconcile through a
      // fresh scan's already_registered flags rather than blindly repeat a mutation.
      if (!status || status >= 500) {
        setResults(null); setChosen({})
        setError('Could not confirm the import. The saved list has been refreshed; scan again to check which devices were added.')
      } else setError(errorMessage(err, 'Could not add these devices.'))
    } finally {
      if (!controller.signal.aborted) setBusy(null)
    }
  }

  return <Dialog open onClose={onClose} title="Add devices" className="max-w-2xl" description="Choose a node to find devices using its installed integrations."
    footer={<><Button onClick={onClose}>Close</Button><Button variant="primary" disabled={!valid || busy === 'scan'} loading={busy === 'import'} onClick={() => void add()}>Add selected ({count})</Button></>}>
    <div className="space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <Select label="Scan using" wrapperClassName="flex-1" value={nodeID} disabled={busy === 'import'}
          options={config.nodes.map((n) => ({ value: n.node_id, label: `${deviceNodeName(n)}${n.online ? '' : ' (offline)'}`, disabled: !n.online }))}
          placeholder={nodeID ? undefined : 'No online node'}
          onValueChange={(value) => { request.current?.abort(); setNodeID(value); setResults(null); setChosen({}); setBusy(null); setError('') }} />
        <Button disabled={!node?.online || busy === 'import'} loading={busy === 'scan'} onClick={() => void scan()}>{results ? 'Scan again' : 'Scan for devices'}</Button>
      </div>
      {!node && <Alert title="No node is online">Start a household node, then reopen discovery to try again.</Alert>}
      {node && <p className="text-small text-text-muted">Discovery runs on {deviceNodeName(node)}’s network. A configured integration may also find cloud devices.</p>}
      {roomsError && <Alert tone="warning">Rooms couldn't be loaded. You can add devices without a room and assign one later.</Alert>}
      {error && <Alert tone="danger">{error}</Alert>}
      {busy === 'scan' && <Alert title={`Scanning with ${node ? deviceNodeName(node) : 'your node'}…`}>This can take up to two minutes. Closing this view stops waiting; the node may finish its scan.</Alert>}
      {results?.length === 0 && <EmptyState title="No devices found" description="Check the integration's setup and the device's discovery mode, or try another node. An empty scan doesn't mean there are no devices on this network." />}
      {!!results?.length && <fieldset disabled={busy === 'import'}>
        <legend className="mb-2 text-small text-text-muted">{results.length} found · Select the devices to add</legend>
        <ul className="divide-y divide-border">{results.map((d) => {
          const selected = chosen[d.entity_id]
          const edit = (patch: Partial<{ name: string; room: string }>) => setChosen((old) => ({ ...old, [d.entity_id]: { ...old[d.entity_id], ...patch } }))
          return <li key={d.entity_id} className="space-y-3 py-4">
            <div className="flex items-center gap-3">
              <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-3">
                <input type="checkbox" aria-label={`Select ${d.name}`} className="size-5 shrink-0 accent-primary" disabled={d.already_registered} checked={!!selected}
                  onChange={(e) => setChosen((old) => { const next = { ...old }; if (e.target.checked) next[d.entity_id] = { name: d.name, room: '' }; else delete next[d.entity_id]; return next })} />
                <span className="min-w-0"><span className="block break-words text-subheading">{d.name}</span><span className="block break-words text-small text-text-muted">{[d.domain.replaceAll('_', ' '), d.protocol, d.manufacturer, d.model].filter(Boolean).join(' · ')}</span></span>
              </label>
              {d.already_registered && (d.existing_device_id
                ? <Button size="sm" onClick={() => onExisting(d.existing_device_id!)}>View added</Button>
                : <Badge>Already added</Badge>)}
            </div>
            {selected && <div className="grid gap-3 pl-8 sm:grid-cols-2">
              <Input label={`Name for ${d.name}`} value={selected.name} onChange={(e) => edit({ name: e.target.value })} />
              <Select label={`Room for ${d.name}`} value={selected.room} options={roomOptions} onValueChange={(room) => edit({ room })} disabled={roomsError} />
            </div>}
          </li>
        })}</ul>
      </fieldset>}
    </div>
  </Dialog>
}

import { useEffect, useId, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { controlDevice, getDeviceState, removeDevice, updateDevice } from '@/api/devices'
import type { Device, DeviceInput, DeviceRoom } from '@/api/devices'
import { Alert, Button, Dialog, Input, Select } from '@/components/ui'
import { errorMessage } from '@/lib/errors'

interface Props { device: Device; rooms: DeviceRoom[]; roomsError: boolean; onClose: () => void; onChanged: () => void }

export default function DeviceDetails({ device, rooms, roomsError, onClose, onChanged }: Props) {
  const [name, setName] = useState(device.name)
  const [room, setRoom] = useState(device.room_id ?? '')
  const [busy, setBusy] = useState(false)
  const [removing, setRemoving] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [input, setInput] = useState<DeviceInput | null>(null)
  const [pin, setPin] = useState('')
  const pinField = useRef<HTMLInputElement>(null)
  useEffect(() => { if (input && !busy) pinField.current?.focus() }, [input, busy])
  const operation = useRef<AbortController | null>(null)
  useEffect(() => () => operation.current?.abort(), [])
  const formID = useId()
  const hh = device.household_id
  const state = useQuery({ queryKey: ['devices', hh, device.id, 'state'], queryFn: ({ signal }) => getDeviceState(hh, device.id, signal), retry: false, refetchOnWindowFocus: false, staleTime: 0 })
  const needsPairing = state.data?.state?.needs_pairing === true || state.data?.ui_hints?.control_type === 'pairing'
  const canPair = device.is_controllable && (needsPairing || device.supported_actions?.some((a) => a.button_action === 'pair_start'))
  const stateError = state.error ? errorMessage(state.error) : state.data?.error
  const roomOptions = [{ value: '', label: 'Unassigned' }, ...rooms.map((r) => ({ value: r.id, label: r.name }))]
  if (room && !roomOptions.some((r) => r.value === room)) roomOptions.push({ value: room, label: device.room_name || 'Current room' })

  const begin = () => {
    operation.current?.abort()
    const controller = new AbortController()
    operation.current = controller
    setBusy(true); setError(''); setNotice('')
    return controller
  }

  const save = async (remove = false) => {
    const controller = begin()
    try {
      if (remove) await removeDevice(hh, device.id, controller.signal)
      else await updateDevice(hh, device.id, { name: name.trim(), room_id: room || null }, controller.signal)
      if (controller.signal.aborted) return
      onChanged(); onClose()
    } catch (err) {
      if (!controller.signal.aborted) setError(errorMessage(err, remove ? 'Could not remove the device.' : 'Could not save changes.'))
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  const pair = async () => {
    const controller = begin()
    const action = input?.follow_up_action ?? 'pair_start'
    const data = input ? { session_id: input.session_id, pin } : undefined
    setInput(null); setPin('')
    try {
      const result = await controlDevice(hh, device.id, action, data, controller.signal)
      if (controller.signal.aborted) return
      if (result.input_required) {
        const prompt = result.input_required
        if (prompt.type !== 'pin' || !prompt.session_id || !prompt.follow_up_action) {
          setError('This integration requested an unsupported setup step. Complete setup through its supported app, then refresh the device status.')
        } else setInput(prompt)
      } else if (result.success) {
        const refreshed = await state.refetch()
        if (controller.signal.aborted) return
        setNotice(refreshed.error || refreshed.data?.error ? 'Pairing completed; live status is currently unavailable.' : 'Pairing completed.')
        onChanged()
      } else setError(result.error || 'Pairing failed. Start pairing again to retry.')
    } catch (err) {
      if (!controller.signal.aborted) setError(errorMessage(err, 'Pairing could not be confirmed. Refresh status before trying again.'))
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }

  return <Dialog open onClose={onClose} title={device.name} description={[device.domain.replaceAll('_', ' '), device.protocol || device.source].join(' · ')}
    footer={removing ? <><Button disabled={busy} onClick={() => setRemoving(false)}>Keep device</Button><Button variant="danger" loading={busy} onClick={() => void save(true)}>Remove from Jarvis</Button></>
      : <><Button onClick={onClose}>Close</Button><Button variant="primary" type="submit" form={formID} disabled={!name.trim() || !!input} loading={busy}>Save changes</Button></>}>
    <div className="space-y-5">
      {error && <Alert tone="danger">{error}</Alert>}
      {notice && <Alert tone="success">{notice}</Alert>}
      {removing ? <Alert tone="warning" title="Remove this device?">This removes its Jarvis registration. It does not factory-reset the physical device. You can discover it again later.</Alert> : <>
        <form id={formID} className="space-y-3" onSubmit={(e) => { e.preventDefault(); void save() }}>
          <Input label="Device name" value={name} onChange={(e) => setName(e.target.value)} required disabled={busy} />
          <Select label="Room" value={room} options={roomOptions} onValueChange={setRoom} disabled={busy || roomsError} hint={roomsError ? 'Rooms could not be loaded; the current assignment is preserved.' : undefined} />
        </form>
        <section aria-label="Device pairing" className="space-y-3 border-t border-border pt-4">
          <h3 className="text-subheading">Connection and pairing</h3>
          {state.isPending ? <p role="status" className="text-small text-text-muted">Checking device status…</p>
            : stateError ? <Alert tone="warning" title="Live status unavailable">{stateError}</Alert>
            : <p className="text-small text-text-muted">{needsPairing ? 'This device needs pairing before Jarvis can control it.' : 'Device status received.'}</p>}
          {input ? <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); void pair() }}>
            <Input ref={pinField} label="Pairing code" hint={input.prompt} value={pin} onChange={(e) => setPin(e.target.value)} autoComplete="off" spellCheck={false} required disabled={busy} />
            <div className="flex flex-wrap gap-2"><Button variant="primary" type="submit" disabled={!pin.trim()} loading={busy}>Submit code</Button><Button disabled={busy} onClick={() => { setInput(null); setPin('') }}>Cancel pairing</Button></div>
          </form> : <div className="flex flex-wrap gap-2">
            {canPair && <Button variant="primary" loading={busy} onClick={() => void pair()}>Pair device</Button>}
            <Button disabled={busy} loading={state.isFetching} onClick={() => void state.refetch()}>Refresh status</Button>
          </div>}
        </section>
        <div className="border-t border-border pt-4"><Button variant="danger" disabled={busy || !!input} onClick={() => { setRemoving(true); setError('') }}>Remove device</Button></div>
      </>}
    </div>
  </Dialog>
}

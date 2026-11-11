import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronRight, Plus, Radio, RefreshCw } from 'lucide-react'
import { useAuth } from '@/hooks/useAuth'
import { defaultDeviceNode, deviceNodeName, getDeviceConfig, listDeviceRooms, listDevices, requestDeviceJob } from '@/api/devices'
import { Alert, Button, Card, EmptyState, ErrorState, Input, List, PageHeader, SkeletonList } from '@/components/ui'
import DeviceDiscovery from '@/components/devices/DeviceDiscovery'
import DeviceDetails from '@/components/devices/DeviceDetails'
import { errorMessage } from '@/lib/errors'

export default function DevicesPage() {
  const { activeHouseholdId: household, state } = useAuth()
  if (!household) return <EmptyState title="Select a household" description="Choose a household to see its devices." />
  // A new household/user gets a new component lifetime: requests, dialogs and PINs cannot leak.
  return <HouseholdDevices key={`${state.user?.id}:${household}`} household={household} superuser={!!state.user?.is_superuser} />
}

function HouseholdDevices({ household, superuser }: { household: string; superuser: boolean }) {
  const qc = useQueryClient()
  const [search, setSearch] = useState('')
  const [discover, setDiscover] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const config = useQuery({ queryKey: ['devices', household, 'config'], queryFn: ({ signal }) => getDeviceConfig(household, signal), retry: false })
  const external = !!config.data?.use_external_devices
  const devices = useQuery({ queryKey: ['devices', household, 'registry'], queryFn: ({ signal }) => listDevices(household, signal), enabled: !!config.data && !external, retry: false })
  const rooms = useQuery({ queryKey: ['devices', household, 'rooms'], queryFn: ({ signal }) => listDeviceRooms(household, signal), enabled: !!config.data && !external, retry: false })
  const node = config.data && defaultDeviceNode(config.data)
  const manager = useQuery({ queryKey: ['devices', household, 'manager', node?.node_id], queryFn: ({ signal }) => requestDeviceJob(node!.node_id, 'device-list', signal), enabled: external && !!node, retry: false, refetchOnWindowFocus: false, staleTime: Infinity })
  const list = external ? manager : devices
  const items = external ? manager.data?.devices ?? [] : devices.data ?? []
  const filtered = items.filter((d) => [d.name, d.room_name, d.protocol, d.domain].join(' ').toLowerCase().includes(search.toLowerCase()))
  const device = devices.data?.find((d) => d.id === selected)
  const changed = () => { void qc.invalidateQueries({ queryKey: ['devices', household, 'registry'] }) }
  const loading = config.isPending || (external ? !!node && manager.isPending : devices.isPending)
  const error = config.error || list.error

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <PageHeader title="Devices" description="The lights, appliances and other devices in your household."
        actions={external
          ? <Button icon={<RefreshCw size={16} aria-hidden />} disabled={!node} loading={manager.isFetching} onClick={() => void manager.refetch()}>Refresh devices</Button>
          : <Button variant="primary" icon={<Plus size={16} aria-hidden />} disabled={!config.data} onClick={() => setDiscover(true)}>Add devices</Button>} />
      {external && <Alert title="Managed by your integrations">{node ? `Listed using ${deviceNodeName(node)}. ` : 'No household node is online. '}Manage these devices in their integration or the mobile app.</Alert>}
      <Input type="search" label="Search devices" placeholder="Name, room or type" value={search} onChange={(e) => setSearch(e.target.value)} />
      <Card flush>
        {error ? <ErrorState title="Couldn't load devices" message={errorMessage(error)} onRetry={() => config.error ? config.refetch() : list.refetch()} />
          : loading ? <SkeletonList rows={4} label="Loading devices" />
          : external && !node ? <EmptyState title="No node is online" description="Start a household node to load its devices, then refresh this page." />
          : filtered.length === 0 ? <EmptyState icon={<Radio size={20} />} title={search ? 'No matching devices' : 'No devices yet'} description={search ? 'Try a different name, room or type.' : external ? 'Check that your device manager is configured on the node.' : 'Add devices to scan with a household node. You can name them and pair them here.'} />
          : <List label="Household devices">{filtered.map((d) => {
            const saved = 'id' in d ? d : undefined
            const content = <>
              <span className="flex min-w-0 flex-1 flex-col gap-1 text-left">
                <span className="break-words text-subheading">{d.name}</span>
                <span className="break-words text-small text-text-muted">{[d.room_name || ('area' in d && d.area) || 'Unassigned', d.domain.replaceAll('_', ' '), d.protocol || ('source' in d && d.source)].filter(Boolean).join(' · ')}</span>
              </span>
              {saved && <ChevronRight size={18} className="shrink-0 text-text-muted" aria-hidden />}
            </>
            return <li key={saved?.id ?? d.entity_id}>{saved
              ? <button type="button" aria-label={`Manage ${d.name}`} className="flex w-full items-center gap-3 px-4 py-4 hover:bg-surface-alt" onClick={() => setSelected(saved.id)}>{content}</button>
              : <div className="flex items-center gap-3 px-4 py-4">{content}</div>}</li>
          })}</List>}
      </Card>
      {!external && <p className="text-small text-text-muted">Discovery uses the integrations installed on your node. {superuser
        ? <Link className="text-primary underline underline-offset-4" to="/admin/store">Browse the Store</Link>
        : 'Use the mobile app or ask your household administrator to set up an integration.'}</p>}
      {discover && config.data && <DeviceDiscovery household={household} config={config.data} rooms={rooms.data ?? []} roomsError={rooms.isError} onClose={() => setDiscover(false)} onChanged={changed} onExisting={(id) => { changed(); setDiscover(false); setSelected(id) }} />}
      {device && <DeviceDetails key={device.id} device={device} rooms={rooms.data ?? []} roomsError={rooms.isError} onClose={() => setSelected(null)} onChanged={changed} />}
    </div>
  )
}

import { useSystemInfo } from '@/hooks/useSystem'
import { Cpu, Database, HardDrive, MemoryStick } from 'lucide-react'
import { formatBytes, formatUptime } from '@/lib/format'

export default function SystemInfoBar() {
  const { data } = useSystemInfo()

  if (!data) return null

  const served = data.listeners?.filter((l) => l.served) ?? []
  const listenerTitle = served.map((l) => `${l.name} :${l.port}`).join('\n')
  const platform = data.arch ? `${data.platform}/${data.arch}` : data.platform

  return (
    <div className="space-y-0.5 border-t border-[var(--color-border)] px-3 py-2 text-[10px] text-[var(--color-text-muted)]">
      <div className="flex items-center gap-1.5">
        <Cpu size={10} />
        <span>{data.cpuCount} cores</span>
        <span className="mx-0.5">|</span>
        <MemoryStick size={10} />
        <span>{Math.round(data.totalMemoryMb / 1024)}GB</span>
      </div>
      {(data.db_bytes !== undefined || data.disk_free_bytes !== undefined) && (
        <div className="flex items-center gap-1.5" title={data.home ? `Data directory: ${data.home}` : undefined}>
          <Database size={10} />
          <span>DB {formatBytes(data.db_bytes)}</span>
          <span className="mx-0.5">|</span>
          <HardDrive size={10} />
          <span>{formatBytes(data.disk_free_bytes)} free</span>
        </div>
      )}
      <div title={data.release ? `${platform} ${data.release}${data.go_version ? ` · ${data.go_version}` : ''}` : undefined}>
        {data.hostname} &middot; {platform}
      </div>
      <div title={listenerTitle || undefined}>
        jarvisd v{data.version} &middot; up {formatUptime(data.uptime)}
        {served.length > 0 && <> &middot; {served.length} listeners</>}
      </div>
    </div>
  )
}

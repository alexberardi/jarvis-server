import { useSystemInfo } from '@/hooks/useSystem'
import { Cpu, HardDrive } from 'lucide-react'

function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  if (days > 0) return `${days}d ${hours}h`
  const minutes = Math.floor((seconds % 3600) / 60)
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

export default function SystemInfoBar() {
  const { data } = useSystemInfo()

  if (!data) return null

  return (
    <div className="border-t border-[var(--color-border)] px-3 py-2 text-[10px] text-[var(--color-text-muted)]">
      <div className="flex items-center gap-1.5">
        <Cpu size={10} />
        <span>{data.cpuCount} cores</span>
        <span className="mx-0.5">|</span>
        <HardDrive size={10} />
        <span>{Math.round(data.totalMemoryMb / 1024)}GB</span>
      </div>
      <div className="mt-0.5">
        {data.hostname} &middot; up {formatUptime(data.uptime)} &middot; v{data.version}
      </div>
    </div>
  )
}

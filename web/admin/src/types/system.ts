/** One jarvisd listener (legacy port), and whether a module serves it in this process. */
export interface ListenerInfo {
  name: string
  port: number
  served: boolean
}

/** GET /api/system/info: the Fastify-era fields plus jarvisd's own (A3, §6.2 #5). */
export interface SystemInfo {
  hostname: string
  platform: string
  release: string
  cpuCount: number
  totalMemoryMb: number
  version: string
  /** Process uptime in seconds. */
  uptime: number
  arch?: string
  go_version?: string
  started_at?: string
  /** jarvisd's data directory. */
  home?: string
  /** SQLite database plus WAL/SHM, in bytes. */
  db_bytes?: number
  /** Free space on the filesystem holding home, in bytes. */
  disk_free_bytes?: number
  listeners?: ListenerInfo[]
}

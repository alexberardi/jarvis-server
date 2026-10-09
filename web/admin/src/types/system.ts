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
  /** Who restarts jarvisd when it exits (AD8). */
  supervisor?: Supervisor
  restart_supported?: boolean
  /** What the admin may offer without probing (AD5, AD8, AD8b). */
  capabilities?: { restart?: boolean; stop?: boolean; self_update?: boolean }
  /** The Stop button (AD8b): absent on a jarvisd without it. */
  stop?: StopInfo
}

export type Supervisor = 'systemd' | 'launchd' | 'windows-service' | 'none'

/** Whether the Stop button works here, why not, and how to start jarvisd again (AD8b). */
export interface StopInfo {
  supported: boolean
  /** Why stop is unavailable (e.g. a unit an older version wrote would restart jarvisd at once). */
  reason?: string
  /** The command that makes it available (`sudo jarvisd service install`). */
  command?: string
  /** What starts jarvisd again once stopped. */
  start_command: string
  /** Where to run it, and whether a reboot also starts jarvisd. */
  start_note: string
}

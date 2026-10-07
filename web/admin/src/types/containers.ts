export interface ContainerPort {
  private: number
  public: number | null
}

export interface ContainerInfo {
  id: string
  name: string
  image: string
  state: string
  status: string
  ports: ContainerPort[]
  labels: Record<string, string>
  created: string
  displayName?: string
  description?: string | null
  category?: string | null
}

export interface ContainerStats {
  cpuPercent: number
  memoryUsageMb: number
  memoryLimitMb: number
  memoryPercent: number
}

export interface ContainersResponse {
  containers: ContainerInfo[]
  error?: string
}

export interface ContainerDetailResponse {
  container: ContainerInfo
  stats: ContainerStats | null
}

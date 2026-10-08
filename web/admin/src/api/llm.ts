/**
 * The model manager (docs/llm/06-model-manager-api.md §5), reached through the admin gateway's
 * `/api/llm/v1/*` pass-through, plus the AD4 prompt-provider BFF route. Shapes mirror the Go
 * types; fields the SPA doesn't read are left out.
 */
import { apiClient } from './client'

// --- labels and kinds ---

export type EngineLabel = 'live' | 'background' | 'embeddings' | 'stt'
export type VoiceLabel = 'tts' | 'speaker'
export type Label = EngineLabel | VoiceLabel
export type ModelKind = 'llm' | 'mmproj' | 'embedding' | 'stt' | 'tts' | 'speaker'

export const ENGINE_LABELS: EngineLabel[] = ['live', 'background', 'embeddings', 'stt']
export const VOICE_LABELS: VoiceLabel[] = ['tts', 'speaker']
export const ALL_LABELS: Label[] = [...ENGINE_LABELS, ...VOICE_LABELS]

/** The model kind each label runs (06 §1). */
export const LABEL_KIND: Record<Label, ModelKind> = {
  live: 'llm',
  background: 'llm',
  embeddings: 'embedding',
  stt: 'stt',
  tts: 'tts',
  speaker: 'speaker',
}

export const LABEL_TITLE: Record<Label, string> = {
  live: 'Live (voice replies)',
  background: 'Background (memory, planning)',
  embeddings: 'Embeddings',
  stt: 'Speech to text',
  tts: 'Text to speech',
  speaker: 'Speaker ID',
}

export function labelsForKind(kind: string): Label[] {
  return ALL_LABELS.filter((l) => LABEL_KIND[l] === kind)
}

// --- hardware ---

export interface Device {
  backend: string
  index: number
  id: string
  name: string
  total_mb: number
  free_mb: number
  integrated?: boolean
}

export interface Hardware {
  os: string
  arch: string
  devices: Device[] | null
  ignored?: Device[] | null
  sources: string[] | null
  flavour: string
  /** Physical RAM in MB (absent when unknown). */
  ram_mb?: number
  detected_at: string
}

export interface Placement {
  gpu_backend: string
  gpu_devices: string
}

export interface EngineBuild {
  kind: string
  build: string
  flavour: string
  path: string
  pinned: boolean
}

export interface EngineInstance {
  name: string
  state: string
  pid: number
  restarts: number
  since: string
  last_error?: string
  output: string[] | null
  kind: string
  flavour: string
  port: number
  model: string
  labels: string[] | null
  args: string[] | null
}

export interface VoiceModel {
  label: string
  id: string
  kind: string
  path: string
  problem?: string
}

export interface HardwareResponse {
  hardware: Hardware
  proposal: Record<string, Placement>
  builds: Record<string, { build: string; flavours: string[] }>
  installed: EngineBuild[] | null
  engines: EngineInstance[] | null
  voice: VoiceModel[] | null
}

export async function getHardware(refresh = false): Promise<HardwareResponse> {
  const { data } = await apiClient.get<HardwareResponse>('/api/llm/v1/hardware', {
    params: refresh ? { refresh: 'true' } : undefined,
  })
  return data
}

export interface FetchEngineResponse {
  kind: string
  flavour: string
  installed: boolean
  path?: string
  job_id?: number
}

export async function fetchEngine(kind: string, flavour: string): Promise<FetchEngineResponse> {
  const { data } = await apiClient.post<FetchEngineResponse>('/api/llm/v1/hardware/engines', { kind, flavour })
  return data
}

// --- catalog and fit ---

export type FitVerdict = 'fits' | 'tight' | 'split' | 'too_big' | 'cpu' | 'in_binary'

export interface Fit {
  verdict: FitVerdict | string
  needed_mb: number
  context: number
  device?: string
  device_mb?: number
  free_mb?: number
  kv_estimated: boolean
  committed_mb?: number
  alongside?: string[]
  /** System RAM, set when the model is judged for the CPU. */
  ram_mb?: number
}

/** Resident is what already sits on a card: an engine label's load, or "other programs". */
export interface Resident {
  labels: string[]
  model: string
  needed_mb: number
  devices?: number[]
}

export interface CatalogEntry {
  id: string
  display: string
  kind: ModelKind | string
  repo?: string
  revision?: string
  url?: string
  archive?: string
  file: string
  size: number
  sha256: string
  mmproj?: string
  context_default?: number
  context_max?: number
  prompt_provider?: string
  thinking?: boolean
  tags?: string[]
  notes?: string
  fit: Fit
  installed: boolean
  state?: string
}

export interface CatalogResponse {
  models: CatalogEntry[]
  recommended: Partial<Record<Label, string>>
  hardware: Hardware
  residents: Resident[] | null
}

export async function getCatalog(): Promise<CatalogResponse> {
  const { data } = await apiClient.get<CatalogResponse>('/api/llm/v1/models/catalog')
  return data
}

// --- Hugging Face ---

export interface RepoFile {
  name: string
  size: number
  sha256?: string
}

export interface HfChoice {
  file: string
  kind: string
  quant?: string
  size: number
  shards: RepoFile[]
  fit: Fit
}

export interface HfRepoResponse {
  repo: string
  revision: string
  gated: boolean
  files: HfChoice[]
}

/** browseRepo lists a pasted repo's installable files. 403 = gated (needs llm.hf_token). */
export async function browseRepo(repo: string, revision?: string): Promise<HfRepoResponse> {
  const path = repo
    .trim()
    .split('/')
    .map((p) => encodeURIComponent(p))
    .join('/')
  const { data } = await apiClient.get<HfRepoResponse>(`/api/llm/v1/models/hf/${path}`, {
    params: revision ? { revision } : undefined,
  })
  return data
}

// --- installs ---

export interface InstallRequest {
  catalog_id?: string
  repo?: string
  file?: string
  revision?: string
  kind?: string
  id?: string
  display?: string
  mmproj_file?: string
  context_default?: number
  with_mmproj?: boolean
  assign?: Label[]
  gpu_backend?: string
}

export type InstallState = 'queued' | 'running' | 'done' | 'failed' | 'cancelled'

export interface Install {
  id: number
  model_id: string
  mmproj_id?: string
  engine_kind?: string
  engine_flavour?: string
  assign: string[] | null
  state: InstallState | string
  phase: string
  bytes_total: number
  bytes_done: number
  error?: string
  note?: string
  job_id?: number
  created_at: string
  updated_at: string
}

export interface InstallResponse {
  install: Install
  existing: boolean
  /** Set when the model won't fit next to what is already on the card; the install still runs. */
  warning?: string
}

export function isActiveInstall(i: Pick<Install, 'state'>): boolean {
  return i.state === 'queued' || i.state === 'running'
}

export async function startInstall(req: InstallRequest): Promise<InstallResponse> {
  const { data } = await apiClient.post<InstallResponse>('/api/llm/v1/models/install', req)
  return data
}

export async function listInstalls(): Promise<Install[]> {
  const { data } = await apiClient.get<{ installs: Install[] | null }>('/api/llm/v1/models/installs')
  return data.installs ?? []
}

export async function getInstall(id: number): Promise<Install> {
  const { data } = await apiClient.get<Install>(`/api/llm/v1/models/installs/${id}`)
  return data
}

export async function cancelInstall(id: number): Promise<Install> {
  const { data } = await apiClient.post<Install>(`/api/llm/v1/models/installs/${id}/cancel`)
  return data
}

// --- installed ---

export interface InstalledModel {
  id: string
  kind: string
  display: string
  catalog_id?: string
  repo?: string
  revision?: string
  files: RepoFile[] | null
  path: string
  size: number
  mmproj_id?: string
  context_default?: number
  prompt_provider?: string
  state: string
  bytes_done: number
  error?: string
  external: boolean
  labels: string[]
}

export interface InstalledResponse {
  models: InstalledModel[]
  disk_bytes: number
  dir: string
}

export async function listInstalled(): Promise<InstalledResponse> {
  const { data } = await apiClient.get<InstalledResponse>('/api/llm/v1/models/installed')
  return data
}

/** deleteModel removes a model. 409 when a label uses it (force=true clears those labels). */
export async function deleteModel(id: string, force = false): Promise<void> {
  await apiClient.delete(`/api/llm/v1/models/installed/${encodeURIComponent(id)}`, {
    params: force ? { force: 'true' } : undefined,
  })
}

export interface RegisterRequest {
  path: string
  kind: string
  id?: string
  display?: string
  mmproj_id?: string
  context_default?: number
}

export async function registerModel(req: RegisterRequest): Promise<InstalledModel> {
  const { data } = await apiClient.post<InstalledModel>('/api/llm/v1/models/installed', req)
  return data
}

// --- labels ---

export interface LabelConfig {
  label: string
  kind: string
  model_kind: string
  engine: string
  shared_with?: string
  model: string
  model_path: string
  mmproj: string
  mmproj_path: string
  context: number
  parallel: number
  gpu_backend: string
  gpu_devices: string
  split_mode: string
  tensor_split: string
  gpu_layers: number
  kv_cache_type: string
  flash_attn: string
  extra_args: string
  /** ID12: auto (the model's catalog flag; never for remote), on or off. */
  fold_system_messages?: string
  /** Whether requests are folded for a strict chat template right now. */
  fold_system_messages_effective?: boolean
  embedding: boolean
  remote_url: string
  remote_model: string
  remote_vision: boolean
  problem?: string
}

export interface Endpoint {
  label: string
  kind: string
  base_url: string
  model: string
  vision: boolean
  embeddings: boolean
  remote: boolean
  context_length: number
  parallel: number
  engine?: string
  degraded?: boolean
  fold_system_messages?: boolean
}

export interface LabelStatus {
  label: EngineLabel | string
  config: LabelConfig
  state: string
  reason?: string
  endpoint?: Endpoint
}

export interface LabelsResponse {
  labels: LabelStatus[]
  voice: VoiceModel[] | null
  engines: EngineInstance[] | null
  proposal: Record<string, Placement>
  recommend: Partial<Record<Label, string>>
  /** One per card whose assigned engines together exceed it (warn, never refuse). */
  warnings: string[] | null
}

export async function getLabels(): Promise<LabelsResponse> {
  const { data } = await apiClient.get<LabelsResponse>('/api/llm/v1/models/labels')
  return data
}

/** LabelUpdate is one label's fields without the settings prefix (06 §3); voice labels take only model. */
export type LabelUpdate = Partial<{
  engine: string
  model: string
  mmproj: string
  context: number
  parallel: number
  gpu_backend: string
  gpu_devices: string
  split_mode: string
  tensor_split: string
  gpu_layers: number
  kv_cache_type: string
  flash_attn: string
  extra_args: string
  fold_system_messages: string
  remote_url: string
  remote_model: string
  remote_api_key: string
  remote_vision: boolean
}>

/** putLabels validates the whole body before writing anything (422 otherwise; I4). */
export async function putLabels(body: Partial<Record<Label, LabelUpdate>>): Promise<LabelsResponse> {
  const { data } = await apiClient.put<LabelsResponse>('/api/llm/v1/models/labels', body)
  return data
}

// --- prompt provider (AD4) ---

export interface PromptProvider {
  /** llm.prompt_provider as set; "" = derive from the live model. */
  value: string
  /** The live model's provider ("" when none). */
  derived: string
  effective: string
  source: 'setting' | 'model' | ''
  /** Effective is a registered provider; a voice turn fails otherwise. */
  valid: boolean
  options: string[] | null
}

export async function getPromptProvider(): Promise<PromptProvider> {
  const { data } = await apiClient.get<PromptProvider>('/api/prompt-provider')
  return data
}

/** setPromptProvider overrides the provider; "" clears the override. */
export async function setPromptProvider(value: string): Promise<PromptProvider> {
  const { data } = await apiClient.put<PromptProvider>('/api/prompt-provider', { value })
  return data
}

// --- Hugging Face token (llm.hf_token, a secret setting) ---

export async function setHfToken(token: string | null): Promise<void> {
  await apiClient.put('/api/settings/llm/llm.hf_token', { value: token })
}

/** hfTokenIsSet reads the masked secret: "********" when set, "" when not (I5). */
export async function hfTokenIsSet(): Promise<boolean> {
  const { data } = await apiClient.get<{
    services: { service_name: string; settings: { key: string; value: unknown }[] }[]
  }>('/api/settings/', { params: { service: 'llm' } })
  const row = data.services
    .find((s) => s.service_name === 'llm')
    ?.settings.find((s) => s.key === 'llm.hf_token')
  return typeof row?.value === 'string' && row.value !== ''
}

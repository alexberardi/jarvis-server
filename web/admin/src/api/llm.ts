/**
 * The model manager (docs/llm/06-model-manager-api.md §5), reached through the admin gateway's
 * `/api/llm/v1/*` pass-through, plus the AD4 prompt-provider BFF route. Shapes mirror the Go
 * types; fields the SPA doesn't read are left out.
 */
import { apiClient } from './client'

// --- labels and kinds ---

export type EngineLabel = 'live' | 'background' | 'embeddings' | 'stt' | 'cloud'
export type VoiceLabel = 'tts' | 'speaker'
export type Label = EngineLabel | VoiceLabel
export type ModelKind = 'llm' | 'mmproj' | 'embedding' | 'stt' | 'tts' | 'speaker'

/** The labels models are installed for; cloud (remote only) takes none. */
export const ENGINE_LABELS: EngineLabel[] = ['live', 'background', 'embeddings', 'stt']
export const VOICE_LABELS: VoiceLabel[] = ['tts', 'speaker']
export const ALL_LABELS: Label[] = [...ENGINE_LABELS, ...VOICE_LABELS]

/** The model kind each label runs (06 §1). */
export const LABEL_KIND: Record<Label, ModelKind> = {
  live: 'llm',
  background: 'llm',
  embeddings: 'embedding',
  stt: 'stt',
  cloud: 'llm',
  tts: 'tts',
  speaker: 'speaker',
}

export const LABEL_TITLE: Record<Label, string> = {
  live: 'Live (voice replies)',
  background: 'Background (memory, planning)',
  embeddings: 'Embeddings',
  stt: 'Speech to text',
  cloud: 'Cloud (optional)',
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
  /** A broken GPU driver detection found (driver_mismatch). */
  gpu_fault?: GPUFault | null
}

/**
 * GPUFault is why a GPU the models expect can't be used: driver_mismatch (an NVIDIA driver
 * update waiting for a reboot) or gpu_unavailable (no usable GPU detected). Those labels are
 * refused, never moved to the CPU.
 */
export interface GPUFault {
  kind: 'driver_mismatch' | 'gpu_unavailable' | string
  kernel_version?: string
  library_version?: string
  labels?: string[] | null
  /** The admin's warning, e.g. "NVIDIA driver updated (kernel …, libraries …): reboot the server." */
  message: string
  /** What requests to the affected labels fail with. */
  user_message: string
  detected_at?: string
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
  /** The resolver's retries of this failed engine. */
  retries: number
  /** Retries ran out: the engine stays failed until Retry or a settings change. */
  gave_up: boolean
  /** Why it gave up at once, when retrying can't help (out of GPU memory). */
  gave_up_reason?: string
  output: string[] | null
  kind: string
  flavour: string
  port: number
  model: string
  labels: string[] | null
  args: string[] | null
}

/**
 * A remote speech label's fallback (the stt label, or the tts voice when tts.provider=remote):
 * the setting, what served lately and why the fallback can't work when it can't.
 */
export interface SpeechFallback {
  /** The setting: off or cpu. */
  mode: string
  /** Requests the local CPU engine served since jarvisd started. */
  fallbacks: number
  /** What served the latest request: remote or local-cpu ("" before any). */
  last_path?: string
  last_remote_error?: string
  last_remote_error_at?: string
  /** Until then requests skip the remote. */
  backoff_until?: string
  /** Why the fallback can't work as set (fallback=cpu with no local model installed). */
  warning?: string
}

export interface VoiceModel {
  label: string
  id: string
  kind: string
  path: string
  problem?: string
  /** A remote tts voice's fallback to local Kokoro; absent while Kokoro speaks everything. */
  fallback?: SpeechFallback
}

export interface HardwareResponse {
  hardware: Hardware
  gpu_fault?: GPUFault | null
  proposal: Record<string, Placement>
  /**
   * unavailable says why a kind can't run on this host (MLX off Apple Silicon, vLLM without an
   * NVIDIA GPU); a Python engine with a big install says its download and peak disk use.
   */
  builds: Record<string, { build: string; flavours: string[]; unavailable?: string; download_bytes?: number; disk_bytes?: number }>
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

/** retryEngine restarts an engine the resolver gave up on, with a fresh retry budget. */
export async function retryEngine(name: string): Promise<void> {
  await apiClient.post(`/api/llm/v1/hardware/engines/${encodeURIComponent(name)}/retry`)
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
  /** A Hugging Face model folder (safetensors) for a Python engine: MLX on Apple Silicon. */
  directory?: boolean
  /** The least memory (MB) the entry is worth installing on; its fit is too_big below it. */
  min_memory_mb?: number
  fit: Fit
  installed: boolean
  state?: string
  /** Why this machine can't run the entry (an MLX build off Apple Silicon). */
  unavailable?: string
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
  /** A Hugging Face model folder (config.json + safetensors), installed as one model. */
  directory?: boolean
  /** The folder's prompt provider from its name and config.json; "" = the install must name one. */
  prompt_provider?: string
  /** The engine that runs the folder here (mlx-lm, vllm, transformers). */
  engine?: string
  /** Why no engine here can run the folder. */
  unavailable?: string
}

export interface HfRepoResponse {
  repo: string
  revision: string
  gated: boolean
  files: HfChoice[]
  /** The prompt providers a folder's install may name. */
  prompt_providers?: string[]
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
  /** A model folder's prompt provider when its config.json doesn't imply one. */
  prompt_provider?: string
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
  /** A Hugging Face model folder, run by a Python engine (MLX, vLLM, Transformers). */
  directory?: boolean
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
  /** vLLM only: the share of each card it preallocates; 0 = sized to the model. */
  gpu_memory_utilization?: number
  /** Engine for a Hugging Face directory model: auto, mlx-lm, transformers or vllm. */
  dir_engine?: string
  /** Transformers only: auto, bfloat16, float16 or float32. */
  dtype?: string
  /** Transformers on CUDA only: none, bnb-4bit or bnb-8bit. */
  quantization?: string
  extra_args: string
  /** ID12: auto (the model's catalog flag; never for remote), on or off. */
  fold_system_messages?: string
  /** Whether requests are folded for a strict chat template right now. */
  fold_system_messages_effective?: boolean
  embedding: boolean
  remote_url: string
  remote_model: string
  remote_vision: boolean
  /** API the remote speaks: auto (anthropic for api.anthropic.com, else openai), openai or anthropic. */
  remote_api?: string
  /** What remote_api resolves to for remote_url: openai or anthropic. */
  remote_api_effective?: string
  /** stt only: how long one remote transcription may take, in milliseconds. */
  remote_timeout_ms?: number
  /** stt only: what a remote label does when the provider fails (off, or cpu). */
  fallback?: string
  problem?: string
  /** Something to know that isn't a problem (a Python engine runs the model text-only). */
  note?: string
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
  /** Remote dialect: openai or anthropic (empty for local engines). */
  api?: string
}

export interface LabelStatus {
  label: EngineLabel | string
  config: LabelConfig
  state: string
  reason?: string
  endpoint?: Endpoint
  /** Why a ready label runs on an engine started for other settings (the GPU can't hold a second copy). */
  note?: string
  /** A remote stt label's fallback state; absent for every other label. */
  fallback?: SpeechFallback
}

export interface LabelsResponse {
  labels: LabelStatus[]
  voice: VoiceModel[] | null
  engines: EngineInstance[] | null
  proposal: Record<string, Placement>
  recommend: Partial<Record<Label, string>>
  /** One per card whose assigned engines together exceed it (warn, never refuse). */
  warnings: string[] | null
  gpu_fault?: GPUFault | null
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
  gpu_memory_utilization: number
  dir_engine: string
  dtype: string
  quantization: string
  extra_args: string
  fold_system_messages: string
  remote_url: string
  remote_model: string
  remote_api_key: string
  remote_vision: boolean
  remote_api: string
  remote_timeout_ms: number
  fallback: string
}>

/** putLabels validates the whole body before writing anything (422 otherwise; I4). */
export async function putLabels(body: Partial<Record<Label, LabelUpdate>>): Promise<LabelsResponse> {
  const { data } = await apiClient.put<LabelsResponse>('/api/llm/v1/models/labels', body)
  return data
}

// --- prompt provider (AD4) ---

/** One LLM mode setting (llm.tool_calling, llm.thinking_switch) as the server resolves it. */
export interface ModeSetting {
  /** The setting as set: "auto" unless the superuser chose one. */
  value: string
  /** The live model's own value ("" when unknown). */
  detected: string
  /** What a turn uses. */
  effective: string
  /** Where effective comes from. */
  source: 'setting' | 'prompt' | 'template' | 'remote' | 'default'
}

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
  tool_calling: ModeSetting
  thinking_switch: ModeSetting
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

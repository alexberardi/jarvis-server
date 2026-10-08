/**
 * The model jobs (AD3b): one setup-wizard step each, the same checklist on Done, the Models
 * page and the dashboard banner. Pure, so components and tests share it.
 */
import type { JobState, SetupJob } from '@/api/auth'
import type { CatalogEntry, CatalogResponse, InstallRequest, Label, ModelKind } from '@/api/llm'

export type JobId = 'llm' | 'stt' | 'voice' | 'speaker' | 'memory'

export interface JobDef {
  id: JobId
  title: string
  /** The label that decides the job's state. */
  label: Label
  /** Labels a confirmed choice is installed for (the primary one first). */
  labels: Label[]
  kind: ModelKind
  required: boolean
  blurb: string
}

/** JOBS in wizard order; ids match the server's jobs and the wizard's step names. */
export const JOBS: JobDef[] = [
  {
    id: 'llm',
    title: 'Language model',
    label: 'live',
    labels: ['live', 'background'],
    kind: 'llm',
    required: true,
    blurb: 'Understands requests and writes the answers. The biggest download, and the one that matters most.',
  },
  {
    id: 'stt',
    title: 'Speech-to-text',
    label: 'stt',
    labels: ['stt'],
    kind: 'stt',
    required: true,
    blurb: 'Turns what you say to a voice node into text. Needed for voice.',
  },
  {
    id: 'voice',
    title: 'Voice',
    label: 'tts',
    labels: ['tts'],
    kind: 'tts',
    required: true,
    blurb: 'How Jarvis sounds when it answers. Needed for voice.',
  },
  {
    id: 'speaker',
    title: 'Voice recognition',
    label: 'speaker',
    labels: ['speaker'],
    kind: 'speaker',
    required: false,
    blurb: 'Tells household members apart by voice, so answers can be personal. Optional.',
  },
  {
    id: 'memory',
    title: 'Memory & search',
    label: 'embeddings',
    labels: ['embeddings'],
    kind: 'embedding',
    required: false,
    blurb: 'Lets Jarvis find related memories and notes. Small, and runs on the CPU. Optional.',
  },
]

export const JOB_IDS: JobId[] = JOBS.map((j) => j.id)

export function jobDef(id: string): JobDef | undefined {
  return JOBS.find((j) => j.id === id)
}

/** jobFor finds a job's server summary (missing when the server didn't send one). */
export function jobFor(jobs: SetupJob[] | undefined, id: JobId): SetupJob {
  const def = jobDef(id)!
  return (
    jobs?.find((j) => j.job === id) ?? {
      job: id,
      label: def.label,
      labels: def.labels,
      required: def.required,
      state: 'missing',
      label_state: 'not_configured',
    }
  )
}

/** underWay: the operator dealt with the job, so a step may move on without installing again. */
export function underWay(state: JobState | string): boolean {
  return state === 'ready' || state === 'loading' || state === 'downloading'
}

/** jobPercent is a downloading job's progress, or null when unknown. */
export function jobPercent(job: SetupJob): number | null {
  const i = job.install
  if (job.state !== 'downloading' || !i || i.bytes_total <= 0) return null
  return Math.min(100, Math.round((i.bytes_done / i.bytes_total) * 100))
}

/** missingRequired lists the required jobs nothing is on the way for (missing or failed). */
export function missingRequired(jobs: SetupJob[] | undefined): JobDef[] {
  if (!jobs) return []
  return JOBS.filter((d) => d.required && !underWay(jobFor(jobs, d.id).state))
}

/** jobStateText is the checklist wording. Missing reads "Skipped" in the wizard, "Not set up" elsewhere. */
export function jobStateText(job: SetupJob, wizard = false): string {
  switch (job.state) {
    case 'ready':
      return 'Ready'
    case 'loading':
      return 'Starting'
    case 'downloading': {
      const pct = jobPercent(job)
      return pct === null ? 'Downloading' : `Downloading ${pct}%`
    }
    case 'failed':
      return 'Failed'
    case 'missing':
      return wizard ? 'Skipped' : 'Not set up'
    default:
      return job.state
  }
}

/**
 * assignFor is what a confirmed choice is installed for: the job's label, plus its extra labels
 * (background with live) while they have no model of their own.
 */
export function assignFor(def: JobDef, current: Partial<Record<Label, string>>): Label[] {
  return def.labels.filter((l) => l === def.label || !current[l])
}

/** requestFor is the install a confirmed choice starts (an llm's vision projector comes with it). */
export function requestFor(def: JobDef, entry: CatalogEntry, current: Partial<Record<Label, string>>): InstallRequest {
  const req: InstallRequest = { catalog_id: entry.id, assign: assignFor(def, current) }
  if (entry.mmproj) req.with_mmproj = true
  return req
}

/** recommendedEntry is the hardware-fit recommendation for a job, if the catalog has one. */
export function recommendedEntry(def: JobDef, catalog: CatalogResponse | undefined): CatalogEntry | undefined {
  const id = catalog?.recommended[def.label]
  return id ? catalog?.models.find((m) => m.id === id && m.kind === def.kind) : undefined
}

/**
 * defaultChoice pre-fills a step: what the job already runs, else what is downloading for it,
 * else the recommendation, else the first catalog entry of its kind.
 */
export function defaultChoice(
  def: JobDef,
  catalog: CatalogResponse | undefined,
  current: Partial<Record<Label, string>>,
  job: SetupJob,
): string {
  const options = catalog?.models.filter((m) => m.kind === def.kind) ?? []
  for (const id of [current[def.label], job.install?.model_id, recommendedEntry(def, catalog)?.id]) {
    if (id && options.some((m) => m.id === id)) return id
  }
  return options[0]?.id ?? ''
}

/**
 * everythingRecommended is "Install everything recommended": one install per job nothing is on
 * the way for, with the recommended model, merged per model (the live model also serves
 * background: one download).
 */
export function everythingRecommended(
  catalog: CatalogResponse,
  jobs: SetupJob[] | undefined,
  current: Partial<Record<Label, string>>,
): { req: InstallRequest; name: string }[] {
  const out: { req: InstallRequest; name: string }[] = []
  for (const def of JOBS) {
    if (underWay(jobFor(jobs, def.id).state)) continue
    const entry = recommendedEntry(def, catalog)
    if (!entry) continue
    const req = requestFor(def, entry, current)
    const same = out.find((o) => o.req.catalog_id === entry.id)
    if (same) same.req.assign = [...new Set([...(same.req.assign ?? []), ...(req.assign ?? [])])]
    else out.push({ req, name: entry.display })
  }
  return out
}

const LANGUAGES: Record<string, string> = {
  a: 'American English',
  b: 'British English',
  e: 'Spanish',
  f: 'French',
  h: 'Hindi',
  i: 'Italian',
  j: 'Japanese',
  p: 'Portuguese (Brazil)',
  z: 'Mandarin Chinese',
}

/** voiceInfo reads a Kokoro voice name ("bm_george"): language, gender and a display name. */
export function voiceInfo(id: string): { language: string; gender: string; name: string } {
  const cut = id.indexOf('_')
  const prefix = cut > 0 ? id.slice(0, cut) : ''
  const rest = cut > 0 ? id.slice(cut + 1) : id
  const name = rest
    .split(/[_-]/)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
  return {
    language: LANGUAGES[prefix.charAt(0)] ?? 'Other',
    gender: prefix.charAt(1) === 'f' ? 'female' : prefix.charAt(1) === 'm' ? 'male' : '',
    name,
  }
}

/** groupVoices groups voice ids by language, keeping the server's order inside each group. */
export function groupVoices(voices: string[]): { language: string; voices: string[] }[] {
  const groups: { language: string; voices: string[] }[] = []
  for (const v of voices) {
    const language = voiceInfo(v).language
    const g = groups.find((x) => x.language === language)
    if (g) g.voices.push(v)
    else groups.push({ language, voices: [v] })
  }
  return groups
}

/** joinTitles reads ["a", "b", "c"] as "a, b and c". */
export function joinTitles(t: string[]): string {
  return t.length <= 1 ? (t[0] ?? '') : `${t.slice(0, -1).join(', ')} and ${t[t.length - 1]}`
}

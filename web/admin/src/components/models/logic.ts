/**
 * Pure helpers behind the Models components, kept apart from the components (fast refresh) so
 * the A7 wizard and the tests can use them without the UI.
 */
import {
  ALL_LABELS,
  LABEL_KIND,
  type CatalogEntry,
  type CatalogResponse,
  type Fit,
  type Install,
  type InstallRequest,
  type Label,
  type LabelConfig,
  type LabelUpdate,
  type LabelsResponse,
} from '@/api/llm'
import { errorMessage, errorStatus } from '@/lib/errors'
import { formatMB } from '@/lib/format'

/** currentModels maps each label to the model it runs now ("" when none). */
export function currentModels(labels: LabelsResponse | undefined): Record<Label, string> {
  const out = Object.fromEntries(ALL_LABELS.map((l) => [l, ''])) as Record<Label, string>
  for (const l of labels?.labels ?? []) {
    if (l.label in out && l.config.engine !== 'off') out[l.label as Label] = l.config.model || ''
  }
  for (const v of labels?.voice ?? []) {
    if (v.label in out && !v.problem) out[v.label as Label] = v.id
  }
  return out
}

/** recommendedFor lists the labels the server recommends this entry for. */
export function recommendedFor(entry: CatalogEntry, recommended: CatalogResponse['recommended']): Label[] {
  return ALL_LABELS.filter((l) => recommended[l] === entry.id && LABEL_KIND[l] === entry.kind)
}

/**
 * defaultAssign pre-selects the labels this entry is recommended for that have no model yet,
 * so installing the recommendation on a fresh box also puts it to work.
 */
export function defaultAssign(
  entry: CatalogEntry,
  recommended: CatalogResponse['recommended'],
  current: Record<Label, string>,
): Label[] {
  return recommendedFor(entry, recommended).filter((l) => !current[l])
}

/**
 * recommendedInstalls is "Install recommended" (§3.3): one install per distinct recommended
 * model that isn't installed, assigned to the labels it is recommended for, with the vision
 * projector when it serves live (LD4).
 */
export function recommendedInstalls(catalog: CatalogResponse): InstallRequest[] {
  const byModel = new Map<string, Label[]>()
  for (const l of ALL_LABELS) {
    const id = catalog.recommended[l]
    if (!id) continue
    byModel.set(id, [...(byModel.get(id) ?? []), l])
  }
  const out: InstallRequest[] = []
  for (const [id, labels] of byModel) {
    const entry = catalog.models.find((m) => m.id === id)
    if (!entry || entry.installed) continue
    const req: InstallRequest = { catalog_id: id, assign: labels }
    if (entry.mmproj && labels.includes('live')) req.with_mmproj = true
    out.push(req)
  }
  return out
}

/** fitSummary is the one-line explanation behind a verdict (co-residency included). */
export function fitSummary(fit: Fit): string {
  if (fit.verdict === 'in_binary') return 'Runs inside jarvisd on the CPU'
  const ram = fit.ram_mb ? ` of ${formatMB(fit.ram_mb)}` : ''
  if (fit.verdict === 'cpu') return `Runs on the CPU (needs about ${formatMB(fit.needed_mb)}${ram} of RAM)`
  if (fit.verdict === 'too_big' && !fit.device && fit.ram_mb) {
    return `Runs on the CPU, but needs about ${formatMB(fit.needed_mb)}${ram} of RAM: too much for this machine`
  }
  const parts = [`Needs about ${formatMB(fit.needed_mb)}`]
  if (fit.context) parts[0] += ` at ${fit.context.toLocaleString()} context`
  if (fit.device) {
    parts.push(`on ${fit.device}${fit.device_mb ? ` (${formatMB(fit.device_mb)})` : ''}`)
  }
  if (fit.committed_mb && fit.alongside?.length) {
    parts.push(`next to ${fit.alongside.join(', ')} using ${formatMB(fit.committed_mb)}`)
  }
  if (fit.kv_estimated) parts.push('KV size estimated')
  return parts.join(', ')
}

/** normalizeRepo accepts "owner/name", a huggingface.co URL, or a URL to a file in the repo. */
export function normalizeRepo(input: string): string {
  let s = input.trim()
  s = s.replace(/^https?:\/\/(www\.)?(huggingface\.co|hf\.co)\//i, '')
  const parts = s.split(/[/?#]/).filter(Boolean)
  return parts.length >= 2 ? `${parts[0]}/${parts[1]}` : s
}

/** isGatedError says a browse or install call failed because the repo needs a Hugging Face token. */
export function isGatedError(err: unknown): boolean {
  return errorStatus(err) === 403 && /gated|private|hf_token|token/i.test(errorMessage(err, ''))
}

/** installNeedsToken says a failed install was refused by Hugging Face (401/403). */
export function installNeedsToken(i: Pick<Install, 'state' | 'error'>): boolean {
  return i.state === 'failed' && /\b(401|403)\b|gated|unauthori[sz]ed|forbidden|hf_token/i.test(i.error ?? '')
}

/** labelDraftDiff keeps only the fields the operator changed from the label's current config. */
export function labelDraftDiff(config: LabelConfig, draft: LabelUpdate): LabelUpdate {
  const out: LabelUpdate = {}
  for (const [k, v] of Object.entries(draft) as [keyof LabelUpdate, unknown][]) {
    if (k === 'remote_api_key') {
      if (typeof v === 'string' && v !== '') (out as Record<string, unknown>)[k] = v
      continue
    }
    if ((config as unknown as Record<string, unknown>)[k] !== v) (out as Record<string, unknown>)[k] = v
  }
  return out
}


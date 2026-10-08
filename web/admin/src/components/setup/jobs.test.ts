import { describe, expect, it } from 'vitest'
import type { SetupJob } from '@/api/auth'
import type { CatalogEntry, CatalogResponse } from '@/api/llm'
import {
  JOBS,
  defaultChoice,
  everythingRecommended,
  jobDef,
  jobFor,
  jobStateText,
  joinTitles,
  missingRequired,
  requestFor,
  voiceInfo,
  groupVoices,
} from './jobs'

const fit = { verdict: 'fits', needed_mb: 1, context: 0, kv_estimated: false }
const e = (id: string, kind: string, over: Partial<CatalogEntry> = {}): CatalogEntry => ({
  id,
  display: id,
  kind,
  file: id,
  size: 100,
  sha256: '',
  fit,
  installed: false,
  ...over,
})

const catalog: CatalogResponse = {
  models: [
    e('qwen3-4b', 'llm'),
    e('qwen3.5-9b', 'llm', { mmproj: 'qwen3.5-9b-mmproj' }),
    e('qwen3.5-9b-mmproj', 'mmproj'),
    e('whisper-base.en', 'stt'),
    e('whisper-small.en', 'stt'),
    e('kokoro', 'tts'),
    e('eres2net', 'speaker'),
    e('minilm', 'embedding'),
  ],
  recommended: {
    live: 'qwen3.5-9b',
    background: 'qwen3.5-9b',
    stt: 'whisper-small.en',
    tts: 'kokoro',
    speaker: 'eres2net',
    embeddings: 'minilm',
  },
  hardware: { os: 'linux', arch: 'amd64', devices: [], sources: [], flavour: 'cuda', detected_at: '' },
  residents: null,
}

const job = (id: string, state: string, over: Partial<SetupJob> = {}): SetupJob => ({
  ...jobFor(undefined, jobDef(id)!.id),
  state,
  ...over,
})

describe('model jobs (AD3b)', () => {
  it('pre-fills a step: in use, then downloading, then recommended', () => {
    const llm = jobDef('llm')!
    expect(defaultChoice(llm, catalog, {}, job('llm', 'missing'))).toBe('qwen3.5-9b')
    expect(
      defaultChoice(llm, catalog, {}, job('llm', 'downloading', { install: { id: 1, model_id: 'qwen3-4b', state: 'running', phase: 'model', bytes_done: 1, bytes_total: 2 } })),
    ).toBe('qwen3-4b')
    expect(defaultChoice(jobDef('stt')!, catalog, { stt: 'whisper-base.en' }, job('stt', 'ready'))).toBe('whisper-base.en')
  })

  it('installs an llm with its projector, and for background while it has no model', () => {
    const llm = jobDef('llm')!
    const nine = catalog.models[1]
    expect(requestFor(llm, nine, {})).toEqual({ catalog_id: 'qwen3.5-9b', assign: ['live', 'background'], with_mmproj: true })
    expect(requestFor(llm, nine, { background: 'qwen3-14b' }).assign).toEqual(['live'])
  })

  it('"install everything" covers the jobs nothing is on the way for, one install per model', () => {
    const jobs = [job('llm', 'missing'), job('stt', 'downloading'), job('voice', 'ready'), job('speaker', 'failed'), job('memory', 'missing')]
    const all = everythingRecommended(catalog, jobs, {})
    expect(all.map((a) => a.req)).toEqual([
      { catalog_id: 'qwen3.5-9b', assign: ['live', 'background'], with_mmproj: true },
      { catalog_id: 'eres2net', assign: ['speaker'] },
      { catalog_id: 'minilm', assign: ['embeddings'] },
    ])
  })

  it('names what voice still lacks', () => {
    const jobs = [job('llm', 'downloading'), job('stt', 'failed'), job('voice', 'missing'), job('speaker', 'missing'), job('memory', 'missing')]
    expect(missingRequired(jobs).map((d) => d.id)).toEqual(['stt', 'voice'])
    expect(joinTitles(missingRequired(jobs).map((d) => d.title))).toBe('Speech-to-text and Voice')
    expect(joinTitles(['a', 'b', 'c'])).toBe('a, b and c')
    expect(missingRequired(undefined)).toEqual([])
  })

  it('words the checklist', () => {
    const dl = job('llm', 'downloading', { install: { id: 1, model_id: 'x', state: 'running', phase: 'model', bytes_done: 42, bytes_total: 100 } })
    expect(jobStateText(dl)).toBe('Downloading 42%')
    expect(jobStateText(job('memory', 'missing'), true)).toBe('Skipped')
    expect(jobStateText(job('memory', 'missing'))).toBe('Not set up')
    expect(JOBS.filter((j) => j.required).map((j) => j.id)).toEqual(['llm', 'stt', 'voice'])
  })

  it('reads Kokoro voice names', () => {
    expect(voiceInfo('bm_george')).toEqual({ language: 'British English', gender: 'male', name: 'George' })
    expect(voiceInfo('af_heart').gender).toBe('female')
    expect(groupVoices(['af_heart', 'bm_george', 'af_bella']).map((g) => [g.language, g.voices.length])).toEqual([
      ['American English', 2],
      ['British English', 1],
    ])
  })
})

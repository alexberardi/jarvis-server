import { describe, expect, it } from 'vitest'
import type { CatalogEntry, CatalogResponse, LabelConfig } from '@/api/llm'
import { defaultAssign, installNeedsToken, labelDraftDiff, normalizeRepo, recommendedInstalls } from './logic'

const fit = { verdict: 'fits', needed_mb: 1, context: 1, kv_estimated: false }
const e = (id: string, kind: string, over: Partial<CatalogEntry> = {}): CatalogEntry => ({
  id,
  display: id,
  kind,
  file: '',
  size: 1,
  sha256: '',
  fit,
  installed: false,
  ...over,
})

describe('models logic', () => {
  it('recommendedInstalls groups labels per model, skips installed ones, adds the projector for live', () => {
    const cat: CatalogResponse = {
      models: [e('big', 'llm', { mmproj: 'big-mmproj' }), e('mini', 'embedding', { installed: true }), e('kokoro', 'tts')],
      recommended: { live: 'big', background: 'big', embeddings: 'mini', tts: 'kokoro' },
      hardware: { os: '', arch: '', devices: [], sources: [], flavour: 'cuda', detected_at: '' },
      residents: [],
    }
    expect(recommendedInstalls(cat)).toEqual([
      { catalog_id: 'big', assign: ['live', 'background'], with_mmproj: true },
      { catalog_id: 'kokoro', assign: ['tts'] },
    ])
  })

  it('defaultAssign leaves labels that already run a model alone', () => {
    const current = { live: 'other', background: '', embeddings: '', stt: '', tts: '', speaker: '' }
    expect(defaultAssign(e('big', 'llm'), { live: 'big', background: 'big' }, current)).toEqual(['background'])
  })

  it('normalizeRepo accepts ids and URLs', () => {
    expect(normalizeRepo(' Qwen/Qwen2.5-0.5B-Instruct-GGUF ')).toBe('Qwen/Qwen2.5-0.5B-Instruct-GGUF')
    expect(normalizeRepo('https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/blob/main/x.gguf')).toBe(
      'Qwen/Qwen2.5-0.5B-Instruct-GGUF',
    )
  })

  it('labelDraftDiff keeps only changes and never sends an empty API key', () => {
    const cfg = { model: 'a', context: 0, remote_url: '' } as LabelConfig
    expect(labelDraftDiff(cfg, { model: 'a', context: 4096, remote_api_key: '' })).toEqual({ context: 4096 })
    expect(labelDraftDiff(cfg, { remote_api_key: 'sk' })).toEqual({ remote_api_key: 'sk' })
  })

  it('installNeedsToken spots Hugging Face auth failures', () => {
    expect(installNeedsToken({ state: 'failed', error: 'GET …: 401 Unauthorized' })).toBe(true)
    expect(installNeedsToken({ state: 'failed', error: 'sha256 mismatch' })).toBe(false)
    expect(installNeedsToken({ state: 'running', error: '403' })).toBe(false)
  })
})

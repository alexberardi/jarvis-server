/**
 * The Models page's Speech section (docs/llm/07-remote-speech.md §4): the remote provider
 * presets (a static catalog; picking one fills the fields, which stay editable) and the STT
 * test, which transcribes a clip bundled in jarvisd through the configured engine.
 */
import { apiClient } from './client'

export interface SpeechPreset {
  id: string
  name: string
  /** Base URL the dialect's paths are appended to ("" for Other: the user types it). */
  url: string
  /** Dialect: openai or elevenlabs. */
  api: string
  stt_model?: string
  tts_model?: string
  tts_voice?: string
  /** Something to know before picking it (cost, data handling). */
  note?: string
}

export interface SpeechPresets {
  stt: SpeechPreset[]
  tts: SpeechPreset[]
}

export async function getSpeechPresets(): Promise<SpeechPresets> {
  const { data } = await apiClient.get<SpeechPresets>('/api/speech/presets')
  return data
}

export interface STTTestResult {
  /** What the engine heard. */
  text: string
  /** Which path answered: local, remote, or local-cpu (the fallback). */
  engine: string
  latency_ms: number
  /** What the bundled clip actually says. */
  clip_says: string
}

/** testSTT transcribes the bundled clip through the saved stt settings. */
export async function testSTT(): Promise<STTTestResult> {
  const { data } = await apiClient.post<STTTestResult>('/api/speech/stt-test')
  return data
}

/** latencyText shows a test's round trip at a readable precision. */
export function latencyText(ms: number): string {
  return ms < 1000 ? `${Math.round(ms)} ms` : `${(ms / 1000).toFixed(1)} s`
}

/** What a speech test's engine value means, for both the STT and the TTS test. */
export function engineText(engine: string | null): string {
  switch (engine) {
    case 'local':
      return 'on this machine'
    case 'remote':
      return 'by the remote provider'
    case 'local-cpu':
      return 'by the local CPU fallback (the remote failed)'
    default:
      return ''
  }
}

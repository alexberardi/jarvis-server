/** Voices and samples for the active TTS provider (the wizard's Voice step and the Models
 * page's Speech section), through the admin BFF. */
import { apiClient } from './client'
import { updateSetting } from './settings'

export interface VoicesResponse {
  /** Active provider: kokoro, or remote. */
  provider: string
  voices: string[]
  /** Display names where the voices are ids (ElevenLabs); keys are entries of voices. */
  names?: Record<string, string>
  /** The voice in use (the default when unset). */
  current: string
  default: string
  /** "service/key" a chosen voice is written to (tts/tts.kokoro_voice or tts/tts.remote_voice). */
  setting: string
  /** The provider lists no voices: any name is accepted. */
  free_text: boolean
}

export async function getVoices(): Promise<VoicesResponse> {
  const { data } = await apiClient.get<VoicesResponse>('/api/tts/voices')
  return data
}

export interface VoiceSample {
  blob: Blob
  /** Which engine rendered it: local, remote, or local-cpu (the fallback answered). */
  engine: string | null
}

/** sampleVoice renders a short phrase in a voice as a WAV blob; 409 until the voice model is installed. */
export async function sampleVoice(voice: string, text?: string): Promise<VoiceSample> {
  const res = await apiClient.post<Blob>('/api/tts/sample', text ? { voice, text } : { voice }, {
    responseType: 'blob',
  })
  return { blob: res.data, engine: (res.headers['x-speech-engine'] as string | undefined) ?? null }
}

/**
 * setVoice makes a voice the one Jarvis speaks with, written to the setting the server names
 * for the active provider (callers that already have a VoicesResponse pass its setting and
 * save the round trip).
 */
export async function setVoice(voice: string, setting?: string): Promise<void> {
  const target = setting ?? (await getVoices()).setting
  const slash = target.indexOf('/')
  const [service, key] = slash > 0 ? [target.slice(0, slash), target.slice(slash + 1)] : ['tts', target]
  await updateSetting(service, key, voice)
}

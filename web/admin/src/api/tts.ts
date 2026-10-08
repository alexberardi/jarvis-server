/** The wizard's Voice step (AD3b): Kokoro voices and a short sample, through the admin BFF. */
import { apiClient } from './client'
import { updateSetting } from './settings'

export interface VoicesResponse {
  voices: string[]
  /** tts.kokoro_voice as set (the default when unset). */
  current: string
  default: string
}

export async function getVoices(): Promise<VoicesResponse> {
  const { data } = await apiClient.get<VoicesResponse>('/api/tts/voices')
  return data
}

/** sampleVoice renders a short phrase in a voice as a WAV blob; 409 until the voice model is installed. */
export async function sampleVoice(voice: string, text?: string): Promise<Blob> {
  const { data } = await apiClient.post<Blob>('/api/tts/sample', text ? { voice, text } : { voice }, {
    responseType: 'blob',
  })
  return data
}

/** setVoice makes a voice the one Jarvis speaks with. */
export async function setVoice(voice: string): Promise<void> {
  await updateSetting('tts', 'tts.kokoro_voice', voice)
}

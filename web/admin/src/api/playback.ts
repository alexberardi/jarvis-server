import { apiClient } from './client'
import type { NowPlaying } from '@/display/nowPlayingModel'

/**
 * What plays in the household (mobile's Now playing): one entry per node that plays, from any
 * package, through the gateway's member routes. cc checks membership of the household (or of the
 * node's household for a cover or a control).
 */
const CC = '/api/cc/api/v0/mobile/playback'

export type NodePlayback = NowPlaying & { node_id: string; node_name: string; cover?: string }

export async function getPlayback(householdId: string): Promise<NodePlayback[]> {
  const { data } = await apiClient.get<{ playing: NodePlayback[] }>(CC, { params: { household_id: householdId } })
  return data.playing ?? []
}

/** controlPlayback sends one action; the answer is the node's read-back (null: it stopped). */
export async function controlPlayback(c: { node_id: string; action: string; volume?: number; position?: number }): Promise<NodePlayback | null> {
  // jarvisd waits up to 3 s for the node's read-back.
  const { data } = await apiClient.post<{ playing: NodePlayback | null }>(`${CC}/control`, c, { timeout: 10000 })
  return data.playing
}

/** fetchCover is the node's cover as a Blob: an <img> can't send the bearer header. */
export async function fetchCover(p: NodePlayback): Promise<Blob> {
  const { data } = await apiClient.get<Blob>(`${CC}/cover`, { params: { node_id: p.node_id, v: p.cover }, responseType: 'blob' })
  return data
}

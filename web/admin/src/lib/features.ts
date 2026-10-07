/**
 * Server features the SPA can't probe without side effects (a POST that restarts or updates
 * jarvisd). The button is shown until the route answers 404 once, then hidden for this page
 * load: an older jarvisd simply doesn't have it. A reload asks again, so an upgraded jarvisd
 * gets its buttons back.
 */
export type Feature = 'restart' | 'update-apply'

const missing = new Set<Feature>()
const listeners = new Set<() => void>()

export function featureMissing(f: Feature): boolean {
  return missing.has(f)
}

export function markFeatureMissing(f: Feature): void {
  if (missing.has(f)) return
  missing.add(f)
  for (const l of listeners) l()
}

/** subscribeFeatures is for useSyncExternalStore. */
export function subscribeFeatures(cb: () => void): () => void {
  listeners.add(cb)
  return () => listeners.delete(cb)
}

/** resetFeatures is for tests. */
export function resetFeatures(): void {
  missing.clear()
  for (const l of listeners) l()
}

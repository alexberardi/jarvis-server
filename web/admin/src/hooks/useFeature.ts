import { useSyncExternalStore } from 'react'
import { featureMissing, subscribeFeatures, type Feature } from '@/lib/features'

/** useFeatureAvailable is false once the feature's route has answered 404 (see lib/features). */
export function useFeatureAvailable(f: Feature): boolean {
  return useSyncExternalStore(subscribeFeatures, () => !featureMissing(f))
}

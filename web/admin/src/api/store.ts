import { apiClient } from './client'

/**
 * The admin Store page (decision 2026-10-30-admin-store-page): a household's Pantry, read through
 * cc (its Pantry URL and pantry.enabled switch apply), and the ordinary per-node install request
 * the mobile app makes.
 */

/** StorePackage is one row of Pantry's package list. */
export interface StorePackage {
  command_name: string
  display_name: string
  description: string
  author: string | null
  latest_version: string
  categories: string[]
  verified: boolean
}

/** StoreDetail is Pantry's detail for a package; git_tag (its latest version's commit) only for an install. */
export interface StoreDetail extends Omit<StorePackage, 'author'> {
  author: { github: string; display_name: string } | null
  github_repo_url: string
  git_tag?: string | null
  targets?: string[] | null
  apt_packages?: string[] | null
  package_type?: 'command' | 'bundle'
  platforms?: string[]
  license?: string | null
  install_count?: number
  danger_rating?: number
  components?: { type: string; name: string }[]
  security_report?: { summary: string; concerns: string[]; recommendation: string } | null
  updated_at?: string
}

/** InstallStatus is a per-node install request's state (cc's poll). */
export interface InstallStatus {
  status: 'pending' | 'restarting' | 'completed' | 'failed' | 'expired'
  error_message: string | null
}

/** BUNDLE_KEY is the household setting holding its default bundle (comma-separated names). */
export const BUNDLE_KEY = 'pantry.default_bundle'

const pantry = (householdId: string) =>
  `/api/cc/api/v0/households/${encodeURIComponent(householdId)}/pantry/commands`

export async function browseStore(householdId: string, q: string): Promise<StorePackage[]> {
  const { data } = await apiClient.get<{ commands: StorePackage[] }>(pantry(householdId), {
    params: { q: q || undefined, per_page: 100 },
  })
  return data.commands ?? []
}

/**
 * getStorePackage is a package's detail. forInstall adds the commit to install, which costs a
 * Pantry install count (cc asks Pantry's /download), so only the install dialog asks for it.
 */
export async function getStorePackage(householdId: string, name: string, forInstall = false): Promise<StoreDetail> {
  const { data } = await apiClient.get<StoreDetail>(`${pantry(householdId)}/${encodeURIComponent(name)}`, {
    params: forInstall ? { install: 1 } : undefined,
  })
  return data
}

const install = (nodeId: string) => `/api/cc/api/v0/nodes/${encodeURIComponent(nodeId)}/package-install`

/** requestInstall asks one node to install a package; cc checks the household, role and Pantry. */
export async function requestInstall(nodeId: string, pkg: StoreDetail): Promise<string> {
  const { data } = await apiClient.post<{ id: string }>(install(nodeId), {
    command_name: pkg.command_name,
    github_repo_url: pkg.github_repo_url,
    git_tag: pkg.git_tag ?? null,
  })
  return data.id
}

export async function pollInstall(nodeId: string, requestId: string): Promise<InstallStatus> {
  const { data } = await apiClient.get<InstallStatus>(`${install(nodeId)}/${encodeURIComponent(requestId)}`)
  return data
}

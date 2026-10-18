// What scripts/e2e hands the suite: the sandbox's sandbox.env (SBX_*), the empty sandbox for
// the setup wizard (E2E_SETUP_*) and where the run's files go (E2E_OUT).
import path from 'node:path'

export function env(name: string): string {
  const v = process.env[name]
  if (!v) throw new Error(`${name} is not set: run the suite with scripts/e2e web (docs/dev/e2e.md)`)
  return v
}

/** outDir is this suite's directory in the run (.e2e/<run>/web). */
export function outDir(): string {
  return process.env.E2E_OUT ?? path.resolve(import.meta.dirname, 'test-results')
}

/** The seeded logins, one per role (scripts/sandbox: SBX_EMAIL and SBX_EMAIL_<ROLE>). */
export const ROLES = ['superuser', 'admin', 'power', 'member'] as const
export type Role = (typeof ROLES)[number]

export function roleEmail(role: Role): string {
  return role === 'superuser' ? env('SBX_EMAIL') : env(`SBX_EMAIL_${role.toUpperCase()}`)
}

/** stateFile is a role's signed-in storage state, written once by global-setup.ts. */
export function stateFile(role: Role): string {
  return path.join(outDir(), 'auth', `${role}.json`)
}

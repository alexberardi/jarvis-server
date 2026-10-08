/** The setup wizard's steps and where it resumes (pure, so the page file only exports a component). */

/**
 * AD3 + AD3a + AD3b: Check → Account → Hardware → one step per model job (Language model,
 * Speech-to-text, Voice, Voice recognition, Memory & search) → Privacy → Done.
 */
export const STEPS = ['check', 'account', 'hardware', 'llm', 'stt', 'voice', 'speaker', 'memory', 'privacy', 'done'] as const
export type Step = (typeof STEPS)[number]

export const TITLES: Record<Step, string> = {
  check: 'Check',
  account: 'Account',
  hardware: 'Hardware',
  llm: 'Language model',
  stt: 'Speech-to-text',
  voice: 'Voice',
  speaker: 'Voice recognition',
  memory: 'Memory & search',
  privacy: 'Privacy',
  done: 'Done',
}

/** Where a reload resumes (this tab only): the steps after Account need the session it made. */
export const STEP_STORAGE_KEY = 'jarvis-admin:setup-step'

function savedStep(): Step | null {
  try {
    const s = sessionStorage.getItem(STEP_STORAGE_KEY)
    return STEPS.includes(s as Step) ? (s as Step) : null
  } catch {
    return null
  }
}

export function saveStep(s: Step | null): void {
  try {
    if (s) sessionStorage.setItem(STEP_STORAGE_KEY, s)
    else sessionStorage.removeItem(STEP_STORAGE_KEY)
  } catch {
    // Storage blocked: a reload starts over, which is safe.
  }
}

export const idx = (s: Step) => STEPS.indexOf(s)

/** The model-job steps (AD3b), whose ids are the server's job ids. */
export const JOB_STEPS: Step[] = ['llm', 'stt', 'voice', 'speaker', 'memory']

/** isResumable: a step the server may record and resume at (Hardware on). */
export function isResumable(s: Step | null): s is Step {
  return s !== null && idx(s) >= idx('hardware')
}

/** 'server': signed in with nothing saved in this tab, so the install decides (A10 F9). */
export type Initial = Step | 'server' | null

/**
 * initialStep picks where the wizard opens. Before the first superuser exists it is Check (or
 * Account when that is where the tab was). Once signed in, a reload resumes where this tab was,
 * never before Hardware; a new tab or browser asks the server ('server'), which knows whether
 * the wizard was finished and how far the install got.
 */
export function initialStep(needsSuperuser: boolean, signedIn: boolean): Initial {
  const saved = savedStep()
  if (!signedIn) {
    if (!needsSuperuser) return null
    return saved === 'account' ? 'account' : 'check'
  }
  if (saved && idx(saved) >= idx('hardware')) return saved
  if (saved || needsSuperuser) return 'hardware'
  return 'server'
}

/** serverStep turns /api/setup/state's setup_step into a step; null when setup is finished. */
export function serverStep(step: string | undefined): Step | null {
  return step && STEPS.includes(step as Step) && idx(step as Step) >= idx('hardware') ? (step as Step) : null
}

import type { RoutineStep } from '@/api/routines'

/** One parameter of a node's command (its available_commands entry). */
export interface CommandParam {
  name: string
  type: string
  description?: string | null
  required?: boolean
  default_value?: string | null
  enum_values?: string[] | null
  /** A live list for the value: "devices", "devices:<domain>", "entities" or "rooms". */
  options_source?: string | null
}

export interface CommandMeta {
  command_name: string
  description: string
  parameters: CommandParam[]
}

// Hints for core commands a node's catalog may predate (mobile's PARAM_META_OVERRIDES); the
// node's own values win.
const CONTROL_DEVICE_ACTIONS = ['turn_on', 'turn_off', 'play', 'pause', 'volume_up', 'volume_down', 'next', 'previous', 'lock', 'unlock', 'set_temperature', 'set_mode', 'set_brightness', 'set_color']
const OVERRIDES: Record<string, Record<string, Partial<CommandParam>>> = {
  control_device: {
    device_name: { options_source: 'devices' },
    entity_id: { options_source: 'entities' },
    action: { enum_values: CONTROL_DEVICE_ACTIONS },
  },
}

const str = (v: unknown) => (typeof v === 'string' ? v : '')

/** commandCatalog reads a node's reported available_commands into name → command. */
export function commandCatalog(available: Record<string, unknown>[]): Record<string, CommandMeta> {
  const out: Record<string, CommandMeta> = {}
  for (const c of available) {
    const name = str(c.command_name)
    if (!name || c.enabled === false) continue
    const ov = OVERRIDES[name] ?? {}
    const params = (Array.isArray(c.parameters) ? c.parameters : []).flatMap((raw): CommandParam[] => {
      const p = raw as Record<string, unknown>
      if (!str(p.name)) return []
      const hint = ov[str(p.name)] ?? {}
      const enums = Array.isArray(p.enum_values) ? p.enum_values.map(String) : []
      return [{
        name: str(p.name),
        type: str(p.type) || str(p.param_type) || 'str',
        description: str(p.description) || null,
        required: p.required === true,
        default_value: p.default_value == null ? null : String(p.default_value),
        enum_values: enums.length ? enums : (hint.enum_values ?? null),
        options_source: str(p.options_source) || hint.options_source || null,
      }]
    })
    out[name] = { command_name: name, description: str(c.description), parameters: params }
  }
  return out
}

/** withRequiredArgs adds a newly chosen command's required parameters (with defaults) to a step. */
export function withRequiredArgs(step: RoutineStep, meta: CommandMeta | undefined): RoutineStep {
  const have = new Set(step.args.map((a) => a.key))
  const add = (meta?.parameters ?? []).filter((p) => p.required && !have.has(p.name)).map((p) => ({ key: p.name, value: p.default_value ?? '' }))
  return add.length ? { ...step, args: [...step.args, ...add] } : step
}

export interface RoutineForm {
  mode: 'simple' | 'advanced'
  name: string
  triggerPhrases: string[]
  utterance: string
  steps: RoutineStep[]
  scheduled: boolean
  scheduleType: 'cron' | 'interval'
  days: string[]
  intervalMinutes: number
  targetNodeId: string | null
}

/**
 * formProblem is why the routine can't be saved yet (mobile's validation gate), or null.
 * simpleOn says whether a node can run Simple routines (its capabilities).
 */
export function formProblem(f: RoutineForm, catalog: Record<string, CommandMeta>, simpleOn: (nodeId: string) => boolean): string | null {
  if (!f.name.trim()) return 'Add a name'
  if (f.triggerPhrases.length === 0) return 'Add at least one trigger phrase'
  if (f.mode === 'simple') {
    if (!f.utterance.trim()) return 'Say what Jarvis should do'
    if ([...f.utterance].length > 5000) return 'Keep the instruction within 5,000 characters'
  } else {
    if (f.steps.length === 0) return 'Add at least one step'
    for (const [i, s] of f.steps.entries()) {
      if (!s.command.trim()) return `Step ${i + 1}: choose a command`
      for (const p of catalog[s.command]?.parameters.filter((p) => p.required) ?? []) {
        const a = s.args.find((x) => x.key === p.name)
        if (!a || !a.value.trim()) return `Step ${i + 1}: “${p.name}” is required`
      }
    }
    const labels = f.steps.map((s) => s.label.trim() || s.command)
    if (new Set(labels).size !== labels.length) return 'Each step needs its own label'
  }
  if (f.scheduled) {
    if (!f.targetNodeId) return 'Schedule: choose a node to run on'
    if (f.mode === 'simple' && !simpleOn(f.targetNodeId)) return 'Schedule: choose a node that can run Simple routines'
    if (f.scheduleType === 'cron' && f.days.length === 0) return 'Schedule: pick at least one day'
    if (f.scheduleType === 'interval' && f.intervalMinutes <= 0) return 'Schedule: pick an interval'
  }
  return null
}

/** cleanSteps is the steps as saved: a missing label is the command, list values drop empty items. */
export function cleanSteps(steps: RoutineStep[]): RoutineStep[] {
  return steps.map((s) => ({
    command: s.command.trim(),
    label: s.label.trim() || s.command.trim(),
    args: s.args
      .filter((a) => a.key.trim())
      .map((a) => {
        try {
          const v = JSON.parse(a.value)
          if (Array.isArray(v)) return { key: a.key, value: JSON.stringify(v.filter((x) => x !== '')) }
        } catch {
          // a plain value
        }
        return a
      }),
  }))
}

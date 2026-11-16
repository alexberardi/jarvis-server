// Ported from jarvis-node-mobile src/types/autonomyCards.ts: keep the two in step.
/**
 * Inbox cards the server's autonomy features write (jarvis-server
 * docs/autonomy). The metadata shape is the server's contract; parse it
 * defensively so an older or newer server never crashes the detail screen.
 */

export type RanStepStatus = 'pending' | 'ok' | 'failed' | 'skipped';

export interface RanCardStep {
  label: string;
  command: string;
  status: RanStepStatus;
  result: string;
}

export interface RanCardRule {
  tool: string;
  effect: string;
  /** household | watch | goal | automation | tool */
  scope: string;
  note: string;
  reason: string;
  rule_id: number | null;
}

export interface RanCardData {
  /** running | waiting | done | partial | failed | stopped */
  state: string;
  /** The finished errand's answer (markdown), or null on older or unfinished cards. */
  answer: string | null;
  source: { kind: string; label: string };
  steps: RanCardStep[];
  rules: RanCardRule[];
}

const STEP_STATUSES: readonly RanStepStatus[] = ['pending', 'ok', 'failed', 'skipped'];

const str = (v: unknown): string => (typeof v === 'string' ? v : '');

const obj = (v: unknown): Record<string, unknown> =>
  v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {};

/** The "Ran on its own" card (`card_type: "autonomy_ran"`), or null for any other item. */
export function parseRanCard(metadata: Record<string, unknown> | null): RanCardData | null {
  if (!metadata || metadata.card_type !== 'autonomy_ran' || !Array.isArray(metadata.steps)) {
    return null;
  }
  const source = obj(metadata.source);
  const steps = (metadata.steps as unknown[]).map((raw): RanCardStep => {
    const s = obj(raw);
    const status = STEP_STATUSES.includes(s.status as RanStepStatus) ? (s.status as RanStepStatus) : 'pending';
    return { label: str(s.label), command: str(s.command), status, result: str(s.result) };
  });
  const rules = (Array.isArray(metadata.rules) ? (metadata.rules as unknown[]) : []).map(
    (raw): RanCardRule => {
      const r = obj(raw);
      return {
        tool: str(r.tool),
        effect: str(r.effect),
        scope: str(r.scope),
        note: str(r.note),
        reason: str(r.reason),
        rule_id: typeof r.rule_id === 'number' ? r.rule_id : null,
      };
    },
  );
  return {
    state: str(metadata.state),
    answer: typeof metadata.answer === 'string' && metadata.answer.trim() ? metadata.answer : null,
    source: { kind: str(source.kind), label: str(source.label) },
    steps,
    rules,
  };
}

export interface WatchAlertData {
  lookingFor: string;
  value: string;
  previous: string | null;
  checkedAt: string | null;
  sourceUrl: string | null;
  sourceLabel: string;
  /** active | paused | done */
  state: string;
}

const strOrNull = (v: unknown): string | null => (typeof v === 'string' && v ? v : null);

/** A fired watch (`card_type: "watch_alert"`), or null for any other item. */
export function parseWatchAlert(metadata: Record<string, unknown> | null): WatchAlertData | null {
  if (!metadata || metadata.card_type !== 'watch_alert' || typeof metadata.value !== 'string') {
    return null;
  }
  return {
    lookingFor: str(metadata.looking_for),
    value: metadata.value,
    previous: strOrNull(metadata.previous),
    checkedAt: strOrNull(metadata.checked_at),
    sourceUrl: strOrNull(metadata.source_url),
    sourceLabel: str(metadata.source_label),
    state: str(metadata.state),
  };
}

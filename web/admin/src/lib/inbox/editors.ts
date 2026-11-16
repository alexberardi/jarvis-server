// Ported from jarvis-node-mobile src/utils/inboxEditors.ts: keep the two in step.
/**
 * Inbox editor metadata parsing — the client half of the editable-fields
 * contract used by interactive inbox cards (phone-call confirm cards, smart
 * replies, ...).
 *
 * Two producer shapes, one parse result:
 *
 * 1. Legacy single editor (shipped 2026-06-11):
 *      metadata.editable_text = { label?, initial, data_key }
 *    Always a multiline text editor. Malformed shapes are IGNORED (no editor,
 *    elements untouched) — that is the shipped behavior and must not change.
 *
 * 2. Typed multi-field editors (2026-07, phone-call confirm cards):
 *      metadata.editor_schema = 2            // optional, min schema needed
 *      metadata.editable_fields = [
 *        { label: "Phone number", initial: "+15551234567",
 *          data_key: "dialed_number", input_type: "tel", required: true },
 *        { label: "Details", initial: "...", data_key: "details",
 *          input_type: "multiline" },
 *      ]
 *    input_type ∈ {"text", "multiline", "tel", "datetime"}; required defaults
 *    to true. "datetime" fields carry/emit an ISO 8601 local-wall-clock string
 *    ("2026-08-11T18:00:00") and render as a native date/time picker
 *    (InboxDetailScreen → EditableDateTimeField); the value stays an ISO string
 *    so it merges into the callback unchanged.
 *
 * FAIL-CLOSED RULE (the difference from legacy): if editable_fields is
 * declared but this build can't fully render it — unknown input_type,
 * malformed entry, or editor_schema newer than SUPPORTED_EDITOR_SCHEMA — the
 * parse returns `unsupported: true` and the screen must disable every
 * interactive element on the card instead of submitting without the field.
 * A phone-call confirm card's editable number is a safety mitigation; an old
 * build silently dropping it and dialing anyway is the failure mode this
 * exists to prevent.
 */

import { parseServerTime as parseServerDate } from '@/lib/time';

export type EditorFieldType = 'text' | 'multiline' | 'tel' | 'datetime';

const KNOWN_FIELD_TYPES: readonly string[] = ['text', 'multiline', 'tel', 'datetime'];

/** Highest editor_schema this build can render. */
export const SUPPORTED_EDITOR_SCHEMA = 2;

export interface EditorField {
  /** Stable identity for React keys / value state — defaults to data_key. */
  key: string;
  label?: string;
  initial: string;
  /** Key in a tapped element's data that the live value replaces. */
  data_key: string;
  input_type: EditorFieldType;
  /** Empty value blocks elements whose data carries data_key. Default true. */
  required: boolean;
  /** True when this field came from legacy metadata.editable_text. */
  legacy: boolean;
}

export interface EditorParseResult {
  fields: EditorField[];
  /**
   * Declared editors this build cannot render — the card must fail closed
   * (all interactive elements disabled, "update the app" message).
   */
  unsupported: boolean;
}

const parseLegacyEditableText = (
  metadata: Record<string, unknown> | null | undefined,
): EditorField | null => {
  const raw = metadata?.editable_text;
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const et = raw as Record<string, unknown>;
  if (typeof et.initial !== 'string') return null;
  if (typeof et.data_key !== 'string' || et.data_key.length === 0) return null;
  if (et.label != null && typeof et.label !== 'string') return null;
  return {
    key: et.data_key,
    label: et.label ?? undefined,
    initial: et.initial,
    data_key: et.data_key,
    input_type: 'multiline',
    required: true,
    legacy: true,
  };
};

const parseTypedField = (raw: unknown): EditorField | null => {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const f = raw as Record<string, unknown>;
  if (typeof f.initial !== 'string') return null;
  if (typeof f.data_key !== 'string' || f.data_key.length === 0) return null;
  if (f.label != null && typeof f.label !== 'string') return null;
  const inputType = f.input_type ?? 'text';
  if (typeof inputType !== 'string' || !KNOWN_FIELD_TYPES.includes(inputType)) {
    return null; // unknown type → caller fails closed
  }
  if (f.required != null && typeof f.required !== 'boolean') return null;
  return {
    key: typeof f.key === 'string' && f.key.length > 0 ? f.key : f.data_key,
    label: f.label ?? undefined,
    initial: f.initial,
    data_key: f.data_key,
    input_type: inputType as EditorFieldType,
    required: f.required ?? true,
    legacy: false,
  };
};

export const parseInboxEditors = (
  metadata: Record<string, unknown> | null | undefined,
): EditorParseResult => {
  const schema = metadata?.editor_schema;
  if (typeof schema === 'number' && schema > SUPPORTED_EDITOR_SCHEMA) {
    return { fields: [], unsupported: true };
  }

  const rawFields = metadata?.editable_fields;
  if (rawFields != null) {
    // Declared but not an array, or empty of valid entries → fail closed.
    if (!Array.isArray(rawFields)) {
      return { fields: [], unsupported: true };
    }
    const fields: EditorField[] = [];
    for (const raw of rawFields) {
      const parsed = parseTypedField(raw);
      if (parsed === null) {
        return { fields: [], unsupported: true };
      }
      fields.push(parsed);
    }
    // Duplicate data_keys make the merge ambiguous — fail closed.
    const keys = new Set(fields.map((f) => f.data_key));
    if (keys.size !== fields.length) {
      return { fields: [], unsupported: true };
    }
    return { fields, unsupported: false };
  }

  const legacy = parseLegacyEditableText(metadata);
  return { fields: legacy ? [legacy] : [], unsupported: false };
};

/**
 * TTL affordance for interactive cards (phone-call plans expire; see
 * `phone_calls.plan_ttl_minutes`). Producers attach
 * `metadata.expires_at = "<ISO 8601>"`; past-expiry cards render an expired
 * state instead of live chips. Malformed values are ignored (no expiry).
 */
export const parseExpiresAt = (
  metadata: Record<string, unknown> | null | undefined,
): Date | null => {
  const raw = metadata?.expires_at;
  if (typeof raw !== 'string' || raw.length === 0) return null;
  // Server-produced: a zone-less value is UTC (command-center's legacy format).
  return parseServerDate(raw);
};

/** True when a callback rejection means the underlying plan/job expired. */
export const isExpiryError = (err: unknown): boolean => {
  const response = (err as { response?: { status?: number; data?: { detail?: unknown } } } | null)?.response;
  if (response?.status === 410) return true;
  const detail = response?.data?.detail;
  const message =
    (typeof detail === 'string' ? detail : undefined) ??
    (err instanceof Error ? err.message : undefined) ??
    '';
  return /expired/i.test(message);
};

// Ported from jarvis-node-mobile src/types/interactiveList.ts: keep the two in step.
/**
 * Interactive List v1 — wire-format types.
 *
 * The payload lives in an inbox item's `metadata` (category
 * "interactive_list") and is the binding contract between SDK builders
 * (jarvis-command-sdk), producers (node commands / Pantry packages) and
 * the mobile renderer. Parsing is permissive per the SDK's wire-format
 * convention — unknown keys ignored, absent keys defaulted — so the
 * schema can grow additively. See prds/generic-interactive-view.md.
 */

// ── Raw wire shapes (as received in metadata) ──────────────────────────────

export interface WireRequiresRecordField {
  command_name: string;
  field: string;
  field_label?: string | null; // optional; fallback: the field name
}

export interface WireRowAction {
  label: string;
  type: string; // only "webview_pick" is renderable in v1
  start_url?: string; // {label} / {value} substitutions
  pattern?: string; // JS-compatible regex, capture group 1 = value
  save?: { command_name: string; field: string };
}

export interface WireRowDefault {
  selected?: boolean;
  quantity?: number; // 1-99; only meaningful for checkbox_stepper
}

export interface WireRow {
  key: string; // REQUIRED, non-empty, unique across the payload
  label: string; // REQUIRED, ≤120 chars
  caption?: string | null; // optional static caption, ≤200 chars
  control?: string; // "none" | "checkbox" | "checkbox_stepper"; default "none"
  default?: WireRowDefault;
  disabled_caption?: string | null; // shown when gated off, ≤200 chars
  requires_record_field?: WireRequiresRecordField | null;
  row_actions?: WireRowAction[]; // ≤2
}

export interface WireSection {
  title?: string | null; // null/absent = untitled flat list
  rows?: WireRow[];
}

export interface WireAction {
  label: string; // {n} substitutes the live selection count
  callback: string;
  style?: string; // "primary" | "secondary" | "destructive"; default "primary"
}

export interface InteractiveListWirePayload {
  type: 'interactive_list';
  version: number; // renderer falls back if missing or > 1
  command_name: string; // callback + record-API target
  title_override?: string | null; // fallback: inbox item title
  empty_text?: string | null; // fallback: "Nothing here yet."
  context?: Record<string, unknown>; // opaque, echoed verbatim in callbacks
  node_id?: string; // CC-injected, never set by producers
  sections: WireSection[]; // 1..6, ≤100 rows total
  actions: WireAction[]; // 1..6
}

// ── Parsed shapes (post-validation, caps applied, defaults filled) ─────────

export type InteractiveActionStyle = 'primary' | 'secondary' | 'destructive';

export interface ParsedGate {
  command_name: string;
  field: string;
  field_label: string | null;
}

export interface ParsedRowAction {
  label: string;
  type: string; // raw — unknown types render as disabled text
  start_url: string | null;
  pattern: string | null;
  save: { command_name: string; field: string } | null;
}

export interface ParsedRow {
  key: string;
  label: string;
  caption: string | null;
  control: string; // raw — unknown values render the row as disabled text
  default_selected: boolean;
  default_quantity: number; // clamped 1-99
  disabled_caption: string | null;
  requires_record_field: ParsedGate | null;
  row_actions: ParsedRowAction[];
}

export interface ParsedSection {
  title: string | null;
  rows: ParsedRow[];
}

export interface ParsedAction {
  label: string;
  callback: string;
  style: InteractiveActionStyle;
}

export interface ParsedInteractiveList {
  command_name: string;
  title_override: string | null;
  empty_text: string | null;
  context: Record<string, unknown>;
  node_id: string | null;
  sections: ParsedSection[];
  actions: ParsedAction[];
}

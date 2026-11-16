// Ported from jarvis-node-mobile src/utils/interactiveList.ts: keep the two in step.
/**
 * Interactive List v1 — pure renderer semantics.
 *
 * Everything in InteractiveListScreen that doesn't need React lives here
 * so the contract's validation / caps / gating / selection rules are
 * unit-testable: payload parsing with caps + truncation, gate computation
 * against fetched record maps, {n}/{label}/{value} substitutions, the
 * collected-state builder and the row-action pattern compiler.
 */
import type {
  InteractiveActionStyle,
  ParsedAction,
  ParsedGate,
  ParsedInteractiveList,
  ParsedRow,
  ParsedRowAction,
  ParsedSection,
} from './interactiveListTypes';

// ── Caps (decision 3, enforced at render on top of SDK validation) ─────────

export const MAX_SECTIONS = 6;
export const MAX_ROWS_TOTAL = 100;
export const MAX_ACTIONS = 6;
export const MAX_ROW_ACTIONS = 2;
export const MAX_LABEL_CHARS = 120;
export const MAX_CAPTION_CHARS = 200;
export const MAX_QUANTITY = 99;

const ACTION_STYLES: InteractiveActionStyle[] = ['primary', 'secondary', 'destructive'];
const SELECTABLE_CONTROLS = ['checkbox', 'checkbox_stepper'];
const KNOWN_CONTROLS = ['none', ...SELECTABLE_CONTROLS];

// ── Quantity (decision 8) ───────────────────────────────────────────────────

/** Parse a stepper text value, clamped to 1-99; blank/garbage falls back to 1. */
export const parseQuantity = (text: string | undefined): number => {
  const parsed = parseInt(text ?? '', 10);
  if (Number.isNaN(parsed)) return 1;
  return Math.max(1, Math.min(parsed, MAX_QUANTITY));
};

/** Clamp a wire `default.quantity` into 1-99; non-numeric falls back to 1. */
const clampDefaultQuantity = (value: unknown): number => {
  if (typeof value !== 'number' || !Number.isFinite(value)) return 1;
  return Math.max(1, Math.min(Math.trunc(value), MAX_QUANTITY));
};

// ── Payload parsing (validation + caps + truncation) ───────────────────────

export type ParsePayloadResult =
  | { payload: ParsedInteractiveList; truncated: boolean }
  | { invalid: true };

const isPlainObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const truncateTo = (
  value: string,
  max: number,
  onTruncate: () => void,
): string => {
  if (value.length <= max) return value;
  onTruncate();
  return value.slice(0, max);
};

const parseGate = (raw: unknown): ParsedGate | null => {
  if (!isPlainObject(raw)) return null;
  if (typeof raw.command_name !== 'string' || raw.command_name.trim().length === 0) return null;
  if (typeof raw.field !== 'string' || raw.field.trim().length === 0) return null;
  return {
    command_name: raw.command_name,
    field: raw.field,
    field_label: typeof raw.field_label === 'string' ? raw.field_label : null,
  };
};

/**
 * Parse + validate an inbox item's metadata into a renderable payload.
 *
 * Fallback cases (→ `{invalid: true}`, the screen replaces itself with
 * InboxDetail): wrong/missing `type`, `version` missing or > 1, missing
 * `command_name`, missing/empty `sections` or `actions`. A valid payload
 * whose sections contain zero rows total is NOT invalid — it renders
 * `empty_text` instead.
 *
 * Caps are enforced here (sections >6, rows >100 total in document order,
 * actions >6, row_actions >2 dropped; labels/captions truncated). Anything
 * dropped or truncated — including malformed/duplicate-key rows — flips
 * the `truncated` flag so the screen can show its one notice line.
 */
export const parsePayload = (
  metadata: Record<string, unknown> | null | undefined,
): ParsePayloadResult => {
  if (!isPlainObject(metadata)) return { invalid: true };
  const raw = metadata as Record<string, unknown>;

  if (raw.type !== 'interactive_list') return { invalid: true };
  if (
    typeof raw.version !== 'number' ||
    !Number.isInteger(raw.version) ||
    raw.version > 1
  ) {
    return { invalid: true };
  }
  if (typeof raw.command_name !== 'string' || raw.command_name.trim().length === 0) {
    return { invalid: true };
  }
  if (!Array.isArray(raw.sections) || raw.sections.length === 0) return { invalid: true };
  if (!Array.isArray(raw.actions) || raw.actions.length === 0) return { invalid: true };

  let truncated = false;
  const markTruncated = () => {
    truncated = true;
  };

  // Sections + rows, in document order, with the row-total cap.
  const seenKeys = new Set<string>();
  let rowCount = 0;
  const sections: ParsedSection[] = [];
  if (raw.sections.length > MAX_SECTIONS) markTruncated();
  for (const rawSection of raw.sections.slice(0, MAX_SECTIONS)) {
    if (!isPlainObject(rawSection)) {
      markTruncated();
      continue;
    }
    const rows: ParsedRow[] = [];
    const rawRows = Array.isArray(rawSection.rows) ? rawSection.rows : [];
    for (const rawRow of rawRows) {
      if (
        !isPlainObject(rawRow) ||
        typeof rawRow.key !== 'string' ||
        rawRow.key.length === 0 ||
        typeof rawRow.label !== 'string' ||
        rawRow.label.length === 0
      ) {
        markTruncated();
        continue;
      }
      if (seenKeys.has(rawRow.key)) {
        // Keys must be unique across the payload — keep the first.
        markTruncated();
        continue;
      }
      if (rowCount >= MAX_ROWS_TOTAL) {
        markTruncated();
        continue;
      }
      seenKeys.add(rawRow.key);
      rowCount += 1;

      const rowActions: ParsedRowAction[] = [];
      if (Array.isArray(rawRow.row_actions)) {
        for (const rawAction of rawRow.row_actions) {
          if (
            !isPlainObject(rawAction) ||
            typeof rawAction.label !== 'string' ||
            rawAction.label.length === 0
          ) {
            markTruncated();
            continue;
          }
          if (rowActions.length >= MAX_ROW_ACTIONS) {
            markTruncated();
            continue;
          }
          const save = isPlainObject(rawAction.save) &&
            typeof rawAction.save.command_name === 'string' &&
            rawAction.save.command_name.length > 0 &&
            typeof rawAction.save.field === 'string' &&
            rawAction.save.field.length > 0
            ? { command_name: rawAction.save.command_name, field: rawAction.save.field }
            : null;
          rowActions.push({
            label: truncateTo(rawAction.label, MAX_LABEL_CHARS, markTruncated),
            type: typeof rawAction.type === 'string' ? rawAction.type : '',
            start_url: typeof rawAction.start_url === 'string' ? rawAction.start_url : null,
            pattern: typeof rawAction.pattern === 'string' ? rawAction.pattern : null,
            save,
          });
        }
      }

      rows.push({
        key: rawRow.key,
        label: truncateTo(rawRow.label, MAX_LABEL_CHARS, markTruncated),
        caption:
          typeof rawRow.caption === 'string'
            ? truncateTo(rawRow.caption, MAX_CAPTION_CHARS, markTruncated)
            : null,
        control: typeof rawRow.control === 'string' ? rawRow.control : 'none',
        default_selected: isPlainObject(rawRow.default) && rawRow.default.selected === true,
        default_quantity: clampDefaultQuantity(
          isPlainObject(rawRow.default) ? rawRow.default.quantity : undefined,
        ),
        disabled_caption:
          typeof rawRow.disabled_caption === 'string'
            ? truncateTo(rawRow.disabled_caption, MAX_CAPTION_CHARS, markTruncated)
            : null,
        requires_record_field: parseGate(rawRow.requires_record_field),
        row_actions: rowActions,
      });
    }
    sections.push({
      title: typeof rawSection.title === 'string' ? rawSection.title : null,
      rows,
    });
  }

  // Actions (bottom bar).
  const actions: ParsedAction[] = [];
  if (raw.actions.length > MAX_ACTIONS) markTruncated();
  for (const rawAction of raw.actions.slice(0, MAX_ACTIONS)) {
    if (
      !isPlainObject(rawAction) ||
      typeof rawAction.label !== 'string' ||
      rawAction.label.length === 0 ||
      typeof rawAction.callback !== 'string' ||
      rawAction.callback.length === 0
    ) {
      markTruncated();
      continue;
    }
    actions.push({
      label: truncateTo(rawAction.label, MAX_LABEL_CHARS, markTruncated),
      callback: rawAction.callback,
      style: ACTION_STYLES.includes(rawAction.style as InteractiveActionStyle) ? (rawAction.style as InteractiveActionStyle) : 'primary',
    });
  }
  // Every declared action was malformed — nothing to tap, same as "actions empty".
  if (actions.length === 0) return { invalid: true };

  return {
    payload: {
      command_name: raw.command_name,
      title_override: typeof raw.title_override === 'string' ? raw.title_override : null,
      empty_text: typeof raw.empty_text === 'string' ? raw.empty_text : null,
      context: isPlainObject(raw.context) ? raw.context : {},
      node_id: typeof raw.node_id === 'string' && raw.node_id.length > 0 ? raw.node_id : null,
      sections,
      actions,
    },
    truncated,
  };
};

// ── Substitutions ───────────────────────────────────────────────────────────

/** Substitute `{n}` in an action label with the live selection count. */
export const substituteLabel = (actionLabel: string, n: number): string =>
  actionLabel.split('{n}').join(String(n));

/**
 * Substitute `{label}` / `{value}` into a row action's start_url
 * (URL-encoded). Returns null when the URL needs `{value}` but no current
 * value exists — the action is hidden in that case (decision 6).
 */
export const substituteUrl = (
  startUrl: string,
  subs: { label: string; value?: string | null },
): string | null => {
  const hasValue = subs.value != null && subs.value.length > 0;
  if (startUrl.includes('{value}') && !hasValue) return null;
  return startUrl
    .split('{label}')
    .join(encodeURIComponent(subs.label))
    .split('{value}')
    .join(hasValue ? encodeURIComponent(subs.value as string) : '');
};

// ── Gating (decision 5) ─────────────────────────────────────────────────────

/**
 * Fetched record data per gate command: command_name → (record key →
 * record.data). A command whose listRecords call failed maps to null —
 * gate state unknown ⇒ unmet, so its rows degrade to disabled while
 * everything else keeps working.
 */
export type FetchedRecords = Record<string, Record<string, Record<string, unknown>> | null>;

export interface GateState {
  met: boolean;
  /** Displayable field value when the gate is met; null otherwise. */
  value: string | null;
}

/**
 * "Non-empty" per the contract: a string with trim().length > 0, or any
 * non-null non-string value. Returns the displayable string, or null
 * when the value doesn't satisfy the gate.
 */
const displayableValue = (value: unknown): string | null => {
  if (value === null || value === undefined) return null;
  if (typeof value === 'string') return value.trim().length > 0 ? value : null;
  return String(value);
};

/**
 * A row with `requires_record_field` is enabled iff the fetched records
 * for that command contain a record whose key === row.key with a
 * non-empty `data[field]`. Session overrides (webview_pick write-backs,
 * keyed by row key) take precedence over fetched records.
 */
export const computeGateState = (
  row: ParsedRow,
  fetched: FetchedRecords,
  overrides: Record<string, string>,
): GateState => {
  const gate = row.requires_record_field;
  if (!gate) return { met: true, value: null };
  const override = overrides[row.key];
  if (typeof override === 'string' && override.trim().length > 0) {
    return { met: true, value: override };
  }
  const records = fetched[gate.command_name];
  const value = displayableValue(records?.[row.key]?.[gate.field]);
  return value !== null ? { met: true, value } : { met: false, value: null };
};

export interface RowRenderState {
  /** Gate met and control understood — tappable/usable. */
  enabled: boolean;
  /** Enabled AND carries selection state (control ≠ none). */
  selectable: boolean;
  /** Caption per contract: gate met → "{field_label ?? field}: {value}",
   *  unmet → disabled_caption ?? caption, otherwise the static caption. */
  caption: string | null;
  /** Forward compat: unknown control values render the row as disabled text. */
  unknownControl: boolean;
}

export const computeRowState = (
  row: ParsedRow,
  fetched: FetchedRecords,
  overrides: Record<string, string>,
): RowRenderState => {
  const gate = computeGateState(row, fetched, overrides);
  const unknownControl = !KNOWN_CONTROLS.includes(row.control);
  const enabled = gate.met && !unknownControl;
  const selectable = enabled && SELECTABLE_CONTROLS.includes(row.control);

  let caption: string | null;
  if (row.requires_record_field && gate.met && gate.value !== null) {
    const label = row.requires_record_field.field_label ?? row.requires_record_field.field;
    caption = `${label}: ${gate.value}`;
  } else if (row.requires_record_field && !gate.met) {
    caption = row.disabled_caption ?? row.caption;
  } else {
    caption = row.caption;
  }

  return { enabled, selectable, caption, unknownControl };
};

export const computeRowStates = (
  payload: ParsedInteractiveList,
  fetched: FetchedRecords,
  overrides: Record<string, string>,
): Record<string, RowRenderState> => {
  const states: Record<string, RowRenderState> = {};
  for (const section of payload.sections) {
    for (const row of section.rows) {
      states[row.key] = computeRowState(row, fetched, overrides);
    }
  }
  return states;
};

// ── Selection (decision 9) ──────────────────────────────────────────────────

/** Default selection: `default.selected === true`, overridden to
 *  deselected when the row's gate is unmet or it isn't selectable. */
export const computeDefaultSelection = (
  payload: ParsedInteractiveList,
  fetched: FetchedRecords,
  overrides: Record<string, string>,
): Set<string> => {
  const selected = new Set<string>();
  for (const section of payload.sections) {
    for (const row of section.rows) {
      const state = computeRowState(row, fetched, overrides);
      if (state.selectable && row.default_selected) selected.add(row.key);
    }
  }
  return selected;
};

/** Stepper seeds (text state, parsed on use): each row's default.quantity. */
export const computeDefaultQuantities = (
  payload: ParsedInteractiveList,
): Record<string, string> => {
  const quantities: Record<string, string> = {};
  for (const section of payload.sections) {
    for (const row of section.rows) {
      quantities[row.key] = String(row.default_quantity);
    }
  }
  return quantities;
};

/** Select-all/clear appears when ANY section has more than 3 selectable rows. */
export const shouldShowSelectAll = (
  payload: ParsedInteractiveList,
  rowStates: Record<string, RowRenderState>,
): boolean =>
  payload.sections.some(
    (section) =>
      section.rows.filter((row) => rowStates[row.key]?.selectable).length > 3,
  );

// ── Collected state (the callback `data.selected` array) ───────────────────

export interface SelectedEntry {
  key: string;
  quantity?: number; // present ONLY for checkbox_stepper rows, clamped 1-99
}

/** Selected rows in document order. Rows with control "none" (or unknown)
 *  never appear; quantity is attached only for checkbox_stepper rows. */
export const buildCollectedState = (
  payload: ParsedInteractiveList,
  selected: Set<string>,
  quantities: Record<string, string>,
): SelectedEntry[] => {
  const entries: SelectedEntry[] = [];
  for (const section of payload.sections) {
    for (const row of section.rows) {
      if (!SELECTABLE_CONTROLS.includes(row.control)) continue;
      if (!selected.has(row.key)) continue;
      if (row.control === 'checkbox_stepper') {
        entries.push({ key: row.key, quantity: parseQuantity(quantities[row.key]) });
      } else {
        entries.push({ key: row.key });
      }
    }
  }
  return entries;
};

// ── webview_pick (decision 6) ───────────────────────────────────────────────

/** Compile a row action's URL pattern. Invalid regex ⇒ null — the row
 *  action is unrenderable and gets hidden. */
export const compileRowActionPattern = (pattern: string | null): RegExp | null => {
  if (pattern === null) return null;
  try {
    return new RegExp(pattern);
  } catch {
    return null;
  }
};

/** Current value of a row action's save.field for this row: the session
 *  gate-override if the user picked one, else the fetched record value. */
export const currentActionValue = (
  row: ParsedRow,
  action: ParsedRowAction,
  fetched: FetchedRecords,
  overrides: Record<string, string>,
): string | null => {
  const override = overrides[row.key];
  if (typeof override === 'string' && override.trim().length > 0) return override;
  if (!action.save) return null;
  return displayableValue(fetched[action.save.command_name]?.[row.key]?.[action.save.field]);
};

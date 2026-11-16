import { describe, expect, it } from 'vitest'
import type {
  ParsedInteractiveList,
  ParsedRow,
  ParsedRowAction,
} from './interactiveListTypes';
import {
  buildCollectedState,
  compileRowActionPattern,
  computeDefaultQuantities,
  computeDefaultSelection,
  computeGateState,
  computeRowState,
  computeRowStates,
  currentActionValue,
  type FetchedRecords,
  parsePayload,
  parseQuantity,
  shouldShowSelectAll,
  substituteLabel,
  substituteUrl,
} from './interactiveList';

// ── Fixtures ────────────────────────────────────────────────────────────────

const wireRow = (overrides: Record<string, unknown> = {}) => ({
  key: 'milk',
  label: 'milk',
  control: 'checkbox_stepper',
  default: { selected: true, quantity: 2 },
  ...overrides,
});

const wireMetadata = (overrides: Record<string, unknown> = {}) => ({
  type: 'interactive_list',
  version: 1,
  command_name: 'export_shopping_list',
  node_id: 'node-1',
  sections: [{ title: 'Regulars', rows: [wireRow()] }],
  actions: [{ label: 'Export {n} items', callback: 'export_selected', style: 'primary' }],
  ...overrides,
});

const parsedRow = (overrides: Partial<ParsedRow> = {}): ParsedRow => ({
  key: 'milk',
  label: 'milk',
  caption: null,
  control: 'checkbox_stepper',
  default_selected: true,
  default_quantity: 2,
  disabled_caption: null,
  requires_record_field: null,
  row_actions: [],
  ...overrides,
});

const gatedRow = (overrides: Partial<ParsedRow> = {}): ParsedRow =>
  parsedRow({
    requires_record_field: {
      command_name: 'export_shopping_list',
      field: 'walmart_item_id',
      field_label: 'ID',
    },
    disabled_caption: 'No Walmart match',
    ...overrides,
  });

const parsedPayload = (rows: ParsedRow[][]): ParsedInteractiveList => ({
  command_name: 'export_shopping_list',
  title_override: null,
  empty_text: null,
  context: {},
  node_id: 'node-1',
  sections: rows.map((sectionRows, i) => ({ title: `Section ${i}`, rows: sectionRows })),
  actions: [{ label: 'Go', callback: 'go', style: 'primary' }],
});

const pickerAction = (overrides: Partial<ParsedRowAction> = {}): ParsedRowAction => ({
  label: 'Find ID',
  type: 'webview_pick',
  start_url: 'https://www.walmart.com/search?q={label}',
  pattern: '/ip/(?:[^/]+/)?(\\d{5,})',
  save: { command_name: 'export_shopping_list', field: 'walmart_item_id' },
  ...overrides,
});

const expectValid = (metadata: Record<string, unknown>) => {
  const result = parsePayload(metadata);
  if ('invalid' in result) throw new Error('expected a valid payload');
  return result;
};

// ── parsePayload: validation / fallback matrix ──────────────────────────────

describe('parsePayload validation', () => {
  it.each([
    ['null metadata', null],
    ['non-object metadata', 'nope' as unknown as Record<string, unknown>],
    ['missing type', wireMetadata({ type: undefined })],
    ['wrong type', wireMetadata({ type: 'alert' })],
    ['missing version', wireMetadata({ version: undefined })],
    ['string version', wireMetadata({ version: '1' })],
    ['non-integer version', wireMetadata({ version: 1.5 })],
    ['version > 1', wireMetadata({ version: 2 })],
    ['missing command_name', wireMetadata({ command_name: undefined })],
    ['empty command_name', wireMetadata({ command_name: '   ' })],
    ['missing sections', wireMetadata({ sections: undefined })],
    ['non-array sections', wireMetadata({ sections: {} })],
    ['empty sections', wireMetadata({ sections: [] })],
    ['missing actions', wireMetadata({ actions: undefined })],
    ['empty actions', wireMetadata({ actions: [] })],
    [
      'all actions malformed',
      wireMetadata({ actions: [{ label: 'x' }, { callback: 'y' }] }),
    ],
  ])('falls back on %s', (_name, metadata) => {
    expect(parsePayload(metadata as Record<string, unknown> | null)).toEqual({
      invalid: true,
    });
  });

  it('accepts a minimal valid payload', () => {
    const { payload, truncated } = expectValid(wireMetadata());
    expect(truncated).toBe(false);
    expect(payload.command_name).toBe('export_shopping_list');
    expect(payload.node_id).toBe('node-1');
    expect(payload.sections).toHaveLength(1);
    expect(payload.sections[0].title).toBe('Regulars');
    expect(payload.sections[0].rows[0]).toMatchObject({
      key: 'milk',
      label: 'milk',
      control: 'checkbox_stepper',
      default_selected: true,
      default_quantity: 2,
    });
    expect(payload.actions).toEqual([
      { label: 'Export {n} items', callback: 'export_selected', style: 'primary' },
    ]);
  });

  it('keeps a valid payload with zero rows total (empty-list state)', () => {
    const { payload, truncated } = expectValid(
      wireMetadata({ sections: [{ rows: [] }], empty_text: 'Nothing to export' }),
    );
    expect(truncated).toBe(false);
    expect(payload.sections[0].rows).toEqual([]);
    expect(payload.empty_text).toBe('Nothing to export');
  });

  it('applies permissive defaults for absent optional fields', () => {
    const { payload } = expectValid(
      wireMetadata({
        node_id: undefined,
        context: undefined,
        sections: [{ rows: [{ key: 'a', label: 'a' }] }],
      }),
    );
    expect(payload.node_id).toBeNull();
    expect(payload.context).toEqual({});
    expect(payload.title_override).toBeNull();
    expect(payload.empty_text).toBeNull();
    expect(payload.sections[0].title).toBeNull();
    expect(payload.sections[0].rows[0]).toMatchObject({
      control: 'none',
      default_selected: false,
      default_quantity: 1,
      caption: null,
      disabled_caption: null,
      requires_record_field: null,
      row_actions: [],
    });
  });

  it('echoes producer context verbatim', () => {
    const { payload } = expectValid(wireMetadata({ context: { provider: 'walmart' } }));
    expect(payload.context).toEqual({ provider: 'walmart' });
  });

  it('defaults unknown action styles to primary and keeps known ones', () => {
    const { payload } = expectValid(
      wireMetadata({
        actions: [
          { label: 'A', callback: 'a', style: 'destructive' },
          { label: 'B', callback: 'b', style: 'secondary' },
          { label: 'C', callback: 'c', style: 'sparkly' },
          { label: 'D', callback: 'd' },
        ],
      }),
    );
    expect(payload.actions.map((a) => a.style)).toEqual([
      'destructive',
      'secondary',
      'primary',
      'primary',
    ]);
  });

  it('drops a malformed gate object (row stays ungated)', () => {
    const { payload } = expectValid(
      wireMetadata({
        sections: [
          { rows: [wireRow({ requires_record_field: { field: 'walmart_item_id' } })] },
        ],
      }),
    );
    expect(payload.sections[0].rows[0].requires_record_field).toBeNull();
  });

  it('keeps gate field_label null when absent', () => {
    const { payload } = expectValid(
      wireMetadata({
        sections: [
          {
            rows: [
              wireRow({
                requires_record_field: { command_name: 'c', field: 'f' },
              }),
            ],
          },
        ],
      }),
    );
    expect(payload.sections[0].rows[0].requires_record_field).toEqual({
      command_name: 'c',
      field: 'f',
      field_label: null,
    });
  });

  it('clamps default quantity into 1-99', () => {
    const { payload } = expectValid(
      wireMetadata({
        sections: [
          {
            rows: [
              wireRow({ key: 'a', default: { selected: true, quantity: 500 } }),
              wireRow({ key: 'b', default: { selected: true, quantity: 0 } }),
              wireRow({ key: 'c', default: { selected: true, quantity: 'two' } }),
            ],
          },
        ],
      }),
    );
    expect(payload.sections[0].rows.map((r) => r.default_quantity)).toEqual([99, 1, 1]);
  });
});

// ── parsePayload: caps + truncation flag ────────────────────────────────────

describe('parsePayload caps and truncation', () => {
  it('drops sections beyond 6', () => {
    const sections = Array.from({ length: 8 }, (_, i) => ({
      title: `S${i}`,
      rows: [wireRow({ key: `row-${i}` })],
    }));
    const { payload, truncated } = expectValid(wireMetadata({ sections }));
    expect(payload.sections).toHaveLength(6);
    expect(truncated).toBe(true);
  });

  it('drops rows beyond 100 total across sections, in document order', () => {
    const rows = (prefix: string, n: number) =>
      Array.from({ length: n }, (_, i) => wireRow({ key: `${prefix}-${i}` }));
    const { payload, truncated } = expectValid(
      wireMetadata({
        sections: [{ rows: rows('a', 60) }, { rows: rows('b', 60) }],
      }),
    );
    expect(payload.sections[0].rows).toHaveLength(60);
    expect(payload.sections[1].rows).toHaveLength(40);
    expect(payload.sections[1].rows[39].key).toBe('b-39');
    expect(truncated).toBe(true);
  });

  it('drops actions beyond 6', () => {
    const actions = Array.from({ length: 7 }, (_, i) => ({
      label: `A${i}`,
      callback: `cb-${i}`,
    }));
    const { payload, truncated } = expectValid(wireMetadata({ actions }));
    expect(payload.actions).toHaveLength(6);
    expect(truncated).toBe(true);
  });

  it('drops row_actions beyond 2', () => {
    const { payload, truncated } = expectValid(
      wireMetadata({
        sections: [
          {
            rows: [
              wireRow({
                row_actions: [
                  pickerAction({ label: 'One' }),
                  pickerAction({ label: 'Two' }),
                  pickerAction({ label: 'Three' }),
                ],
              }),
            ],
          },
        ],
      }),
    );
    expect(payload.sections[0].rows[0].row_actions.map((a) => a.label)).toEqual([
      'One',
      'Two',
    ]);
    expect(truncated).toBe(true);
  });

  it('truncates label to 120 and captions to 200 chars', () => {
    const { payload, truncated } = expectValid(
      wireMetadata({
        sections: [
          {
            rows: [
              wireRow({
                label: 'x'.repeat(150),
                caption: 'y'.repeat(250),
                disabled_caption: 'z'.repeat(250),
              }),
            ],
          },
        ],
      }),
    );
    const row = payload.sections[0].rows[0];
    expect(row.label).toHaveLength(120);
    expect(row.caption).toHaveLength(200);
    expect(row.disabled_caption).toHaveLength(200);
    expect(truncated).toBe(true);
  });

  it('drops malformed rows (missing key/label) and flags truncation', () => {
    const { payload, truncated } = expectValid(
      wireMetadata({
        sections: [
          {
            rows: [
              wireRow(),
              { label: 'no key' },
              { key: 'no-label' },
              'not even an object',
            ],
          },
        ],
      }),
    );
    expect(payload.sections[0].rows.map((r) => r.key)).toEqual(['milk']);
    expect(truncated).toBe(true);
  });

  it('drops duplicate row keys, keeping the first', () => {
    const { payload, truncated } = expectValid(
      wireMetadata({
        sections: [
          { rows: [wireRow({ label: 'first' })] },
          { rows: [wireRow({ label: 'second' })] },
        ],
      }),
    );
    expect(payload.sections[0].rows[0].label).toBe('first');
    expect(payload.sections[1].rows).toEqual([]);
    expect(truncated).toBe(true);
  });

  it('does not flag truncation when everything fits', () => {
    const { truncated } = expectValid(wireMetadata());
    expect(truncated).toBe(false);
  });
});

// ── Substitutions ───────────────────────────────────────────────────────────

describe('substituteLabel', () => {
  it('substitutes {n} with the selection count', () => {
    expect(substituteLabel('Export {n} items', 3)).toBe('Export 3 items');
  });

  it('substitutes every occurrence', () => {
    expect(substituteLabel('{n} of {n}', 2)).toBe('2 of 2');
  });

  it('leaves labels without the placeholder untouched', () => {
    expect(substituteLabel('Approve', 5)).toBe('Approve');
  });
});

describe('substituteUrl', () => {
  it('substitutes {label} URL-encoded', () => {
    expect(
      substituteUrl('https://w.com/search?q={label}', { label: 'whole milk & eggs' }),
    ).toBe('https://w.com/search?q=whole%20milk%20%26%20eggs');
  });

  it('substitutes {value} URL-encoded', () => {
    expect(
      substituteUrl('https://w.com/ip/{value}', { label: 'milk', value: 'a/b' }),
    ).toBe('https://w.com/ip/a%2Fb');
  });

  it('returns null when the URL needs {value} but none exists', () => {
    expect(substituteUrl('https://w.com/ip/{value}', { label: 'milk' })).toBeNull();
    expect(
      substituteUrl('https://w.com/ip/{value}', { label: 'milk', value: null }),
    ).toBeNull();
    expect(
      substituteUrl('https://w.com/ip/{value}', { label: 'milk', value: '' }),
    ).toBeNull();
  });

  it('passes through URLs without placeholders', () => {
    expect(substituteUrl('https://w.com/deals', { label: 'milk' })).toBe(
      'https://w.com/deals',
    );
  });
});

// ── Gating ──────────────────────────────────────────────────────────────────

describe('computeGateState', () => {
  const fetchedWith = (data: Record<string, unknown>): FetchedRecords => ({
    export_shopping_list: { milk: data },
  });

  it('is met for rows without a gate', () => {
    expect(computeGateState(parsedRow(), {}, {})).toEqual({ met: true, value: null });
  });

  it('is met when the record field has a non-empty string', () => {
    expect(
      computeGateState(gatedRow(), fetchedWith({ walmart_item_id: '12345' }), {}),
    ).toEqual({ met: true, value: '12345' });
  });

  it('is met for non-string non-null values (stringified)', () => {
    expect(
      computeGateState(gatedRow(), fetchedWith({ walmart_item_id: 12345 }), {}),
    ).toEqual({ met: true, value: '12345' });
    expect(
      computeGateState(gatedRow(), fetchedWith({ walmart_item_id: false }), {}),
    ).toEqual({ met: true, value: 'false' });
  });

  it('is unmet for null, missing, empty and whitespace-only values', () => {
    expect(computeGateState(gatedRow(), fetchedWith({ walmart_item_id: null }), {}).met).toBe(false);
    expect(computeGateState(gatedRow(), fetchedWith({}), {}).met).toBe(false);
    expect(computeGateState(gatedRow(), fetchedWith({ walmart_item_id: '' }), {}).met).toBe(false);
    expect(computeGateState(gatedRow(), fetchedWith({ walmart_item_id: '   ' }), {}).met).toBe(false);
  });

  it('is unmet when there is no record for the row key', () => {
    expect(
      computeGateState(gatedRow(), { export_shopping_list: {} }, {}).met,
    ).toBe(false);
  });

  it('degrades to unmet when the fetch for that command failed', () => {
    expect(computeGateState(gatedRow(), { export_shopping_list: null }, {}).met).toBe(false);
    expect(computeGateState(gatedRow(), {}, {}).met).toBe(false);
  });

  it('prefers session overrides over fetched records', () => {
    expect(
      computeGateState(gatedRow(), fetchedWith({ walmart_item_id: '111' }), {
        milk: '999',
      }),
    ).toEqual({ met: true, value: '999' });
  });

  it('meets the gate from an override even with no fetched records', () => {
    expect(computeGateState(gatedRow(), {}, { milk: '777' })).toEqual({
      met: true,
      value: '777',
    });
  });
});

describe('computeRowState', () => {
  it('enables ungated rows and computes selectability per control', () => {
    expect(computeRowState(parsedRow({ control: 'checkbox' }), {}, {})).toMatchObject({
      enabled: true,
      selectable: true,
      unknownControl: false,
    });
    expect(computeRowState(parsedRow({ control: 'none' }), {}, {})).toMatchObject({
      enabled: true,
      selectable: false,
    });
  });

  it('renders unknown controls as disabled text (forward compat)', () => {
    expect(computeRowState(parsedRow({ control: 'radio' }), {}, {})).toMatchObject({
      enabled: false,
      selectable: false,
      unknownControl: true,
    });
  });

  it('builds the met-gate caption as "{field_label}: {value}"', () => {
    const state = computeRowState(
      gatedRow({ caption: 'static caption' }),
      { export_shopping_list: { milk: { walmart_item_id: '12345' } } },
      {},
    );
    expect(state.enabled).toBe(true);
    expect(state.caption).toBe('ID: 12345');
  });

  it('falls back to the field name when field_label is absent', () => {
    const row = gatedRow();
    row.requires_record_field!.field_label = null;
    const state = computeRowState(
      row,
      { export_shopping_list: { milk: { walmart_item_id: '12345' } } },
      {},
    );
    expect(state.caption).toBe('walmart_item_id: 12345');
  });

  it('shows disabled_caption ?? caption when the gate is unmet', () => {
    expect(computeRowState(gatedRow(), {}, {}).caption).toBe('No Walmart match');
    expect(
      computeRowState(gatedRow({ disabled_caption: null, caption: 'plain' }), {}, {})
        .caption,
    ).toBe('plain');
  });

  it('keeps the static caption for ungated rows', () => {
    expect(computeRowState(parsedRow({ caption: 'hello' }), {}, {}).caption).toBe(
      'hello',
    );
  });
});

// ── Selection defaults + select-all threshold ───────────────────────────────

describe('computeDefaultSelection', () => {
  it('selects default.selected rows whose gates are met', () => {
    const payload = parsedPayload([
      [
        parsedRow({ key: 'a', control: 'checkbox', default_selected: true }),
        parsedRow({ key: 'b', control: 'checkbox', default_selected: false }),
        gatedRow({ key: 'c', control: 'checkbox', default_selected: true }),
        parsedRow({ key: 'd', control: 'none', default_selected: true }),
      ],
    ]);
    const selected = computeDefaultSelection(payload, {}, {});
    expect(selected).toEqual(new Set(['a'])); // c gated-unmet, d not selectable
  });

  it('includes gated rows once the gate is met (fetched or override)', () => {
    const payload = parsedPayload([
      [gatedRow({ key: 'c', control: 'checkbox', default_selected: true })],
    ]);
    expect(
      computeDefaultSelection(
        payload,
        { export_shopping_list: { c: { walmart_item_id: '1' } } },
        {},
      ),
    ).toEqual(new Set(['c']));
    expect(computeDefaultSelection(payload, {}, { c: '99999' })).toEqual(new Set(['c']));
  });
});

describe('computeDefaultQuantities', () => {
  it('seeds stepper text state from default_quantity', () => {
    const payload = parsedPayload([
      [parsedRow({ key: 'a', default_quantity: 5 }), parsedRow({ key: 'b' })],
    ]);
    expect(computeDefaultQuantities(payload)).toEqual({ a: '5', b: '2' });
  });
});

describe('shouldShowSelectAll', () => {
  const rows = (n: number, control = 'checkbox') =>
    Array.from({ length: n }, (_, i) => parsedRow({ key: `r-${i}`, control }));

  it('shows when any section has more than 3 selectable rows', () => {
    const payload = parsedPayload([rows(2), rows(4).map((r, i) => ({ ...r, key: `s2-${i}` }))]);
    expect(shouldShowSelectAll(payload, computeRowStates(payload, {}, {}))).toBe(true);
  });

  it('hides at exactly 3 selectable rows per section', () => {
    const payload = parsedPayload([rows(3)]);
    expect(shouldShowSelectAll(payload, computeRowStates(payload, {}, {}))).toBe(false);
  });

  it('does not count disabled or control-none rows', () => {
    const payload = parsedPayload([
      [
        ...rows(3),
        gatedRow({ key: 'gated', control: 'checkbox' }), // unmet ⇒ not selectable
        parsedRow({ key: 'info', control: 'none' }),
      ],
    ]);
    expect(shouldShowSelectAll(payload, computeRowStates(payload, {}, {}))).toBe(false);
  });
});

// ── Collected state ─────────────────────────────────────────────────────────

describe('buildCollectedState', () => {
  it('emits selected rows in document order with quantity only for steppers', () => {
    const payload = parsedPayload([
      [
        parsedRow({ key: 'a', control: 'checkbox_stepper' }),
        parsedRow({ key: 'b', control: 'checkbox' }),
      ],
      [parsedRow({ key: 'c', control: 'checkbox_stepper' })],
    ]);
    const selected = new Set(['c', 'b', 'a']); // insertion order ≠ document order
    const entries = buildCollectedState(payload, selected, { a: '2', c: '7' });
    expect(entries).toEqual([
      { key: 'a', quantity: 2 },
      { key: 'b' },
      { key: 'c', quantity: 7 },
    ]);
    expect('quantity' in entries[1]).toBe(false);
  });

  it('never includes control "none" or unknown-control rows', () => {
    const payload = parsedPayload([
      [
        parsedRow({ key: 'info', control: 'none' }),
        parsedRow({ key: 'future', control: 'radio' }),
        parsedRow({ key: 'a', control: 'checkbox' }),
      ],
    ]);
    expect(
      buildCollectedState(payload, new Set(['info', 'future', 'a']), {}),
    ).toEqual([{ key: 'a' }]);
  });

  it('clamps stepper quantities and defaults garbage to 1', () => {
    const payload = parsedPayload([
      [
        parsedRow({ key: 'a', control: 'checkbox_stepper' }),
        parsedRow({ key: 'b', control: 'checkbox_stepper' }),
        parsedRow({ key: 'c', control: 'checkbox_stepper' }),
      ],
    ]);
    const entries = buildCollectedState(payload, new Set(['a', 'b', 'c']), {
      a: '500',
      b: '',
      // c has no text state at all
    });
    expect(entries).toEqual([
      { key: 'a', quantity: 99 },
      { key: 'b', quantity: 1 },
      { key: 'c', quantity: 1 },
    ]);
  });
});

describe('parseQuantity', () => {
  it('clamps to 1-99 and falls back to 1 for blank/garbage', () => {
    expect(parseQuantity('5')).toBe(5);
    expect(parseQuantity('0')).toBe(1);
    expect(parseQuantity('150')).toBe(99);
    expect(parseQuantity('')).toBe(1);
    expect(parseQuantity(undefined)).toBe(1);
    expect(parseQuantity('abc')).toBe(1);
  });
});

// ── webview_pick helpers ────────────────────────────────────────────────────

describe('compileRowActionPattern', () => {
  it('compiles a valid pattern with a capture group', () => {
    const regex = compileRowActionPattern('/ip/(?:[^/]+/)?(\\d{5,})');
    expect(regex).toBeInstanceOf(RegExp);
    const match = 'https://www.walmart.com/ip/great-value-milk/10450114'.match(regex!);
    expect(match?.[1]).toBe('10450114');
  });

  it('returns null for invalid regex or missing pattern', () => {
    expect(compileRowActionPattern('([')).toBeNull();
    expect(compileRowActionPattern(null)).toBeNull();
  });
});

describe('currentActionValue', () => {
  const fetched: FetchedRecords = {
    export_shopping_list: { milk: { walmart_item_id: '12345' } },
  };

  it('prefers the session override', () => {
    expect(currentActionValue(parsedRow(), pickerAction(), fetched, { milk: '999' })).toBe(
      '999',
    );
  });

  it('falls back to the fetched record value for save.command_name/save.field', () => {
    expect(currentActionValue(parsedRow(), pickerAction(), fetched, {})).toBe('12345');
  });

  it('returns null with no override, no record, or no save target', () => {
    expect(currentActionValue(parsedRow(), pickerAction(), {}, {})).toBeNull();
    expect(
      currentActionValue(parsedRow(), pickerAction({ save: null }), fetched, {}),
    ).toBeNull();
    expect(
      currentActionValue(
        parsedRow(),
        pickerAction(),
        { export_shopping_list: { milk: { walmart_item_id: '  ' } } },
        {},
      ),
    ).toBeNull();
  });
});

import { describe, expect, it } from 'vitest'
import {
  isExpiryError,
  parseExpiresAt,
  parseInboxEditors,
  SUPPORTED_EDITOR_SCHEMA,
} from './editors';

describe('parseInboxEditors', () => {
  describe('legacy editable_text (shipped behavior — must not change)', () => {
    it('parses the valid single-editor shape', () => {
      const result = parseInboxEditors({
        editable_text: { label: 'Reply', initial: 'draft', data_key: 'body' },
      });
      expect(result.unsupported).toBe(false);
      expect(result.fields).toHaveLength(1);
      expect(result.fields[0]).toMatchObject({
        label: 'Reply',
        initial: 'draft',
        data_key: 'body',
        input_type: 'multiline',
        required: true,
        legacy: true,
      });
    });

    it.each([
      ['not an object', 'nope'],
      ['array', [1]],
      ['missing initial', { data_key: 'body' }],
      ['missing data_key', { initial: 'x' }],
      ['empty data_key', { initial: 'x', data_key: '' }],
      ['non-string label', { initial: 'x', data_key: 'body', label: 42 }],
    ])('ignores malformed shapes (%s) — NOT fail-closed for legacy', (_name, et) => {
      const result = parseInboxEditors({ editable_text: et });
      expect(result.fields).toHaveLength(0);
      expect(result.unsupported).toBe(false);
    });

    it('returns no fields for absent metadata', () => {
      expect(parseInboxEditors(null)).toEqual({ fields: [], unsupported: false });
      expect(parseInboxEditors({})).toEqual({ fields: [], unsupported: false });
    });
  });

  describe('typed editable_fields', () => {
    const phoneCard = {
      editor_schema: 2,
      editable_fields: [
        {
          label: 'Phone number',
          initial: '+15551234567',
          data_key: 'dialed_number',
          input_type: 'tel',
        },
        {
          label: 'Details',
          initial: 'Table for 4 at 7pm',
          data_key: 'details',
          input_type: 'multiline',
          required: false,
        },
      ],
    };

    it('parses typed fields with defaults (input_type=text, required=true)', () => {
      const result = parseInboxEditors({
        editable_fields: [{ initial: 'x', data_key: 'k' }],
      });
      expect(result.unsupported).toBe(false);
      expect(result.fields[0]).toMatchObject({
        input_type: 'text',
        required: true,
        legacy: false,
        key: 'k',
      });
    });

    it('parses the phone confirm-card shape', () => {
      const result = parseInboxEditors(phoneCard);
      expect(result.unsupported).toBe(false);
      expect(result.fields.map((f) => f.input_type)).toEqual(['tel', 'multiline']);
      expect(result.fields[0].required).toBe(true);
      expect(result.fields[1].required).toBe(false);
    });

    it('prefers editable_fields over a co-present legacy editable_text', () => {
      const result = parseInboxEditors({
        ...phoneCard,
        editable_text: { initial: 'legacy', data_key: 'body' },
      });
      expect(result.fields.map((f) => f.data_key)).toEqual(['dialed_number', 'details']);
    });
  });

  describe('fail-closed guard (the PRD min-schema requirement)', () => {
    it('unknown input_type → unsupported', () => {
      const result = parseInboxEditors({
        editable_fields: [
          { initial: '', data_key: 'sig', input_type: 'signature_pad' },
        ],
      });
      expect(result.unsupported).toBe(true);
      expect(result.fields).toHaveLength(0);
    });

    it('malformed entry among valid ones → unsupported (never partial-render)', () => {
      const result = parseInboxEditors({
        editable_fields: [
          { initial: 'ok', data_key: 'a' },
          { data_key: 'missing-initial' },
        ],
      });
      expect(result.unsupported).toBe(true);
    });

    it('editor_schema newer than supported → unsupported', () => {
      const result = parseInboxEditors({
        editor_schema: SUPPORTED_EDITOR_SCHEMA + 1,
        editable_fields: [{ initial: 'x', data_key: 'k' }],
      });
      expect(result.unsupported).toBe(true);
    });

    it('editor_schema at or below supported is fine', () => {
      const result = parseInboxEditors({
        editor_schema: SUPPORTED_EDITOR_SCHEMA,
        editable_fields: [{ initial: 'x', data_key: 'k' }],
      });
      expect(result.unsupported).toBe(false);
    });

    it('editable_fields that is not an array → unsupported', () => {
      expect(parseInboxEditors({ editable_fields: {} }).unsupported).toBe(true);
    });

    it('duplicate data_keys → unsupported (ambiguous merge)', () => {
      const result = parseInboxEditors({
        editable_fields: [
          { initial: 'a', data_key: 'k' },
          { initial: 'b', data_key: 'k' },
        ],
      });
      expect(result.unsupported).toBe(true);
    });
  });
});

describe('parseExpiresAt', () => {
  it('parses a valid ISO timestamp', () => {
    const d = parseExpiresAt({ expires_at: '2026-07-19T12:00:00Z' });
    expect(d?.toISOString()).toBe('2026-07-19T12:00:00.000Z');
  });

  it('reads a zone-less expires_at as UTC (command-center legacy format)', () => {
    const d = parseExpiresAt({ expires_at: '2026-07-19T12:00:00.123456' });
    expect(d?.toISOString()).toBe('2026-07-19T12:00:00.123Z');
  });

  it('honors an offset', () => {
    const d = parseExpiresAt({ expires_at: '2026-07-19T08:00:00-04:00' });
    expect(d?.toISOString()).toBe('2026-07-19T12:00:00.000Z');
  });

  it.each([
    ['absent', {}],
    ['null metadata', null],
    ['empty string', { expires_at: '' }],
    ['garbage', { expires_at: 'soon-ish' }],
    ['number', { expires_at: 1234 }],
  ])('returns null for %s', (_name, metadata) => {
    expect(parseExpiresAt(metadata as Record<string, unknown>)).toBeNull();
  });
});

describe('isExpiryError', () => {
  it('detects HTTP 410', () => {
    expect(isExpiryError({ response: { status: 410 } })).toBe(true);
  });

  it('detects an "expired" detail from CC', () => {
    expect(
      isExpiryError({
        response: { status: 400, data: { detail: 'Callback job expired' } },
      }),
    ).toBe(true);
  });

  it('detects an "expired" plain Error message', () => {
    expect(isExpiryError(new Error('plan expired — ask again'))).toBe(true);
  });

  it('rejects unrelated errors', () => {
    expect(isExpiryError(new Error('network down'))).toBe(false);
    expect(isExpiryError({ response: { status: 500, data: { detail: 'boom' } } })).toBe(false);
    expect(isExpiryError(undefined)).toBe(false);
  });
});

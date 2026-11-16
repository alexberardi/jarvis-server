import { describe, expect, it } from 'vitest'
import { parseRanCard, parseWatchAlert } from './autonomyCards';

const sample = {
  household_id: 'hh',
  card_type: 'autonomy_ran',
  state: 'running',
  workflow_id: 'wf_1',
  plan_id: 'pl_1',
  source: { kind: 'errand', label: 'Errand' },
  steps: [
    { label: 'Search the web', command: 'quick_search', status: 'ok', result: '5 results' },
    { label: 'Send it', command: 'send_email', status: 'pending', result: '' },
  ],
  rules: [
    {
      tool: 'quick_search',
      effect: 'allow',
      scope: 'tool',
      rule_id: null,
      note: '',
      reason: 'the tool declares run_without_confirmation',
    },
  ],
  interactive_elements: [],
};

describe('parseRanCard', () => {
  it('returns null without card_type', () => {
    const rest: Record<string, unknown> = { ...sample };
    delete rest.card_type;
    expect(parseRanCard(rest)).toBeNull();
    expect(parseRanCard(null)).toBeNull();
    expect(parseRanCard({ ...sample, card_type: 'watch_alert' })).toBeNull();
  });

  it('returns null when steps is not an array', () => {
    expect(parseRanCard({ ...sample, steps: 'nope' })).toBeNull();
    expect(parseRanCard({ ...sample, steps: undefined })).toBeNull();
  });

  it('parses a full sample', () => {
    expect(parseRanCard(sample)).toEqual({
      state: 'running',
      answer: null,
      source: { kind: 'errand', label: 'Errand' },
      steps: [
        { label: 'Search the web', command: 'quick_search', status: 'ok', result: '5 results' },
        { label: 'Send it', command: 'send_email', status: 'pending', result: '' },
      ],
      rules: [
        {
          tool: 'quick_search',
          effect: 'allow',
          scope: 'tool',
          rule_id: null,
          note: '',
          reason: 'the tool declares run_without_confirmation',
        },
      ],
    });
  });

  it('keeps a string answer', () => {
    expect(parseRanCard({ ...sample, answer: 'The **Bambino Plus** wins.' })?.answer).toBe(
      'The **Bambino Plus** wins.',
    );
  });

  it('treats a missing, blank or non-string answer as null', () => {
    expect(parseRanCard(sample)?.answer).toBeNull();
    expect(parseRanCard({ ...sample, answer: '  ' })?.answer).toBeNull();
    expect(parseRanCard({ ...sample, answer: 42 })?.answer).toBeNull();
  });

  it('defaults missing rules to []', () => {
    const rest: Record<string, unknown> = { ...sample };
    delete rest.rules;
    expect(parseRanCard(rest)?.rules).toEqual([]);
  });

  it('maps an unknown step status to pending', () => {
    const card = parseRanCard({
      ...sample,
      steps: [{ label: 'x', command: 'x', status: 'exploded', result: '' }],
    });
    expect(card?.steps[0].status).toBe('pending');
  });
});

const watchSample = {
  household_id: 'hh',
  card_type: 'watch_alert',
  watch_id: 'wt_1',
  state: 'active',
  looking_for: 'the price to drop below 100 on example.com',
  value: '$99',
  previous: '$129',
  checked_at: '2026-10-16T14:00:00Z',
  source_url: 'https://www.example.com/item',
  source_label: 'example.com',
  interactive_elements: [],
};

describe('parseWatchAlert', () => {
  it('returns null without card_type or value', () => {
    const rest: Record<string, unknown> = { ...watchSample };
    delete rest.card_type;
    expect(parseWatchAlert(rest)).toBeNull();
    expect(parseWatchAlert(null)).toBeNull();
    expect(parseWatchAlert({ ...watchSample, card_type: 'autonomy_ran' })).toBeNull();
    expect(parseWatchAlert({ ...watchSample, value: undefined })).toBeNull();
    expect(parseWatchAlert({ ...watchSample, value: 99 })).toBeNull();
  });

  it('parses a full sample', () => {
    expect(parseWatchAlert(watchSample)).toEqual({
      lookingFor: 'the price to drop below 100 on example.com',
      value: '$99',
      previous: '$129',
      checkedAt: '2026-10-16T14:00:00Z',
      sourceUrl: 'https://www.example.com/item',
      sourceLabel: 'example.com',
      state: 'active',
    });
  });

  it('keeps a null previous and source_url as null', () => {
    const parsed = parseWatchAlert({
      ...watchSample,
      previous: null,
      source_url: null,
      checked_at: undefined,
    });
    expect(parsed?.previous).toBeNull();
    expect(parsed?.sourceUrl).toBeNull();
    expect(parsed?.checkedAt).toBeNull();
    expect(parsed?.value).toBe('$99');
  });
});

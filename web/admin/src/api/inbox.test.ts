import { describe, expect, it } from 'vitest'
import { confirmationOf, sourcesOf } from './inbox'
import type { InboxItem } from './inbox'

const item = (metadata: Record<string, unknown> | null, category = 'deep_research'): InboxItem => ({
  id: 'i1',
  user_id: 7,
  household_id: 'h1',
  title: 't',
  summary: 's',
  body: 'b',
  category,
  source_service: 'cc',
  metadata,
  is_read: false,
  created_at: '2026-10-13T10:00:00',
})

describe('sourcesOf', () => {
  it('keeps http(s) sources only, titled', () => {
    expect(
      sourcesOf(
        item({
          sources: [
            { title: 'A', url: 'https://a.example/x' },
            { url: 'http://b.example' },
            { title: 'evil', url: 'javascript:alert(1)' },
            'junk',
          ],
        }),
      ),
    ).toEqual([
      { title: 'A', url: 'https://a.example/x' },
      { title: 'http://b.example', url: 'http://b.example' },
    ])
    expect(sourcesOf(item(null))).toEqual([])
  })
})

describe('confirmationOf', () => {
  it('reads the node action a confirmation runs', () => {
    const c = confirmationOf(
      item(
        { node_id: 'n1', command_name: 'send_email', draft: 'hi', actions: [{ button_text: 'Send', button_action: 'send', button_type: 'primary' }] },
        'confirmation',
      ),
    )
    expect(c).toEqual({
      nodeId: 'n1',
      commandName: 'send_email',
      context: { draft: 'hi' },
      actions: [{ button_text: 'Send', button_action: 'send', button_type: 'primary' }],
    })
  })
  it('is null without a node, a command or buttons', () => {
    expect(confirmationOf(item({ command_name: 'x', actions: [{ button_text: 'a', button_action: 'a' }] }))).toBeNull()
    expect(confirmationOf(item({ node_id: 'n', command_name: 'x', actions: [] }))).toBeNull()
  })
})

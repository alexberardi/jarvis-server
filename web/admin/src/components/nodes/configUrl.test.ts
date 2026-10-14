import { describe, expect, it } from 'vitest'
import { configURLFor } from './configUrl'

describe('configURLFor', () => {
  it('uses the page host and the config port', () => {
    expect(configURLFor('jarvis.lan', 7700)).toBe('http://jarvis.lan:7700')
    expect(configURLFor('192.0.2.10', 17700)).toBe('http://192.0.2.10:17700')
  })

  it('brackets an IPv6 host', () => {
    expect(configURLFor('2001:db8::1', 7700)).toBe('http://[2001:db8::1]:7700')
    expect(configURLFor('[2001:db8::1]', 7700)).toBe('http://[2001:db8::1]:7700')
  })

  it('gives nothing for loopback or an empty host', () => {
    for (const h of ['localhost', '127.0.0.1', '::1', '[::1]', '']) expect(configURLFor(h, 7700)).toBeUndefined()
  })
})

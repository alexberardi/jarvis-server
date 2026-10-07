import { AxiosError, AxiosHeaders } from 'axios'
import { describe, expect, it } from 'vitest'
import { errorMessage, errorStatus } from './errors'

function axiosErr(status: number, data: unknown): AxiosError {
  const config = { headers: new AxiosHeaders() }
  return new AxiosError('Request failed', 'ERR_BAD_RESPONSE', config, null, {
    status,
    statusText: '',
    headers: {},
    config,
    data,
  })
}

describe('errorMessage', () => {
  it('reads a {detail} string', () => {
    expect(errorMessage(axiosErr(401, { detail: 'Setup token required' }))).toBe('Setup token required')
  })

  it('joins a 422 detail array with field paths', () => {
    const err = axiosErr(422, {
      detail: [
        { type: 'missing', loc: ['body', 'value'], msg: 'Field required' },
        { type: 'x', loc: ['body', 'live', 'context'], msg: 'Input should be a valid integer' },
      ],
    })
    expect(errorMessage(err)).toBe('value: Field required; live.context: Input should be a valid integer')
  })

  it('reads the settings router {error: {message}}', () => {
    expect(errorMessage(axiosErr(404, { error: { message: 'Unknown setting' } }))).toBe('Unknown setting')
  })

  it('prefers message over a bare error code (cc validation shape)', () => {
    const err = axiosErr(400, { error: 'validation_error', message: 'Request validation failed.' })
    expect(errorMessage(err)).toBe('Request validation failed.')
  })

  it('reads a legacy {error} string', () => {
    expect(errorMessage(axiosErr(500, { error: 'boom' }))).toBe('boom')
  })

  it('falls back with the status when the body says nothing', () => {
    expect(errorMessage(axiosErr(502, ''), 'Save failed')).toBe('Save failed (HTTP 502)')
  })

  it('uses Error.message for non-HTTP errors', () => {
    expect(errorMessage(new Error('network down'))).toBe('network down')
    expect(errorMessage(undefined, 'nope')).toBe('nope')
  })

  it('errorStatus returns the HTTP status', () => {
    expect(errorStatus(axiosErr(403, {}))).toBe(403)
    expect(errorStatus(new Error('x'))).toBeUndefined()
  })
})

import { isAxiosError } from 'axios'

/**
 * errorMessage turns whatever a failed call threw into one line for the operator.
 *
 * jarvisd answers errors in a few shapes, and the SPA must read all of them:
 * - `{detail: "..."}`: httpx errors (auth, llm, the admin BFF)
 * - `{detail: [{loc, msg, type}]}`: 422 validation arrays (FastAPI-shaped)
 * - `{error: {message}}`: the platform settings router
 * - `{error: "..."}` / `{message}`: older Fastify-era and cc shapes
 */
export function errorMessage(err: unknown, fallback = 'Something went wrong'): string {
  if (isAxiosError(err)) {
    const fromBody = messageFromBody(err.response?.data)
    if (fromBody) return fromBody
    if (err.response) return `${fallback} (HTTP ${err.response.status})`
    return err.message || fallback
  }
  const fromBody = messageFromBody(err)
  if (fromBody) return fromBody
  if (err instanceof Error && err.message) return err.message
  if (typeof err === 'string' && err) return err
  return fallback
}

/** errorStatus is the HTTP status of a failed axios call, or undefined. */
export function errorStatus(err: unknown): number | undefined {
  return isAxiosError(err) ? err.response?.status : undefined
}

function messageFromBody(body: unknown): string | null {
  if (typeof body === 'string') return body.trim() || null
  if (!body || typeof body !== 'object' || body instanceof Error) return null
  const b = body as Record<string, unknown>

  const detail = b.detail
  if (typeof detail === 'string' && detail) return detail
  if (Array.isArray(detail) && detail.length > 0) {
    const parts = detail.map(validationLine).filter((s): s is string => Boolean(s))
    if (parts.length > 0) return parts.join('; ')
  }

  const error = b.error
  if (typeof error === 'string' && error) {
    // cc's validation shape: {error: "validation_error", message, details: [...]}.
    if (typeof b.message === 'string' && b.message) return b.message
    return error
  }
  if (error && typeof error === 'object') {
    const msg = (error as Record<string, unknown>).message
    if (typeof msg === 'string' && msg) return msg
  }

  if (typeof b.message === 'string' && b.message) return b.message
  return null
}

function validationLine(item: unknown): string | null {
  if (typeof item === 'string') return item
  if (!item || typeof item !== 'object') return null
  const it = item as { msg?: unknown; loc?: unknown }
  if (typeof it.msg !== 'string') return null
  if (Array.isArray(it.loc)) {
    // Drop the "body"/"query"/"path" location root; keep the field path.
    const path = it.loc.filter(
      (p, i) => !(i === 0 && (p === 'body' || p === 'query' || p === 'path')),
    )
    if (path.length > 0) return `${path.join('.')}: ${it.msg}`
  }
  return it.msg
}

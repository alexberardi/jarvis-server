const LOOPBACK = new Set(['localhost', '127.0.0.1', '::1', '[::1]'])

/**
 * configURLFor is the address a computer passes to `jarvis-agent enroll --server`: the host the
 * admin page was opened on, with jarvisd's config listener port. Undefined for a loopback or
 * empty host, since that name means nothing to another computer.
 */
export function configURLFor(hostname: string, configPort: number): string | undefined {
  if (!hostname || LOOPBACK.has(hostname)) return undefined
  const host = hostname.includes(':') && !hostname.startsWith('[') ? `[${hostname}]` : hostname
  return `http://${host}:${configPort}`
}

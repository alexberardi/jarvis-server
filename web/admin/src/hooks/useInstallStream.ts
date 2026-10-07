import { useState, useCallback, useRef } from 'react'

export interface StreamLine {
  stream: 'stdout' | 'stderr'
  text: string
  timestamp: number
}

export interface StreamState {
  lines: StreamLine[]
  running: boolean
  done: boolean
  exitCode: number | null
  error: string | null
  redirect: string | null
}

/**
 * Hook for consuming SSE streams from install endpoints (pull, start).
 *
 * Returns a `run(url)` function that returns a Promise which resolves
 * when the stream completes (with the exit code) or rejects on error.
 */
export function useInstallStream() {
  const [state, setState] = useState<StreamState>({
    lines: [],
    running: false,
    done: false,
    exitCode: null,
    error: null,
    redirect: null,
  })
  const eventSourceRef = useRef<EventSource | null>(null)

  const run = useCallback((url: string, onEvent?: (data: Record<string, unknown>) => void): Promise<number> => {
    // Clean up any existing connection
    eventSourceRef.current?.close()

    setState({
      lines: [],
      running: true,
      done: false,
      exitCode: null,
      error: null,
      redirect: null,
    })

    return new Promise<number>((resolve, reject) => {
      const es = new EventSource(url)
      eventSourceRef.current = es

      es.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data) as Record<string, unknown>

          // Forward all events to the optional callback
          if (onEvent) {
            onEvent(data)
          }

          if ('done' in data) {
            setState((prev) => ({
              ...prev,
              running: false,
              done: true,
              exitCode: data.code as number,
              redirect: (data.redirect as string) ?? null,
            }))
            es.close()
            if (data.code === 0) {
              resolve(data.code as number)
            } else {
              const errMsg = data.error ? String(data.error) : `Process exited with code ${data.code as number}`
              reject(new Error(errMsg))
            }
            return
          }

          if ('stream' in data && 'text' in data) {
            const line: StreamLine = {
              stream: data.stream as 'stdout' | 'stderr',
              text: data.text as string,
              timestamp: Date.now(),
            }

            setState((prev) => ({
              ...prev,
              lines: [...prev.lines, line],
            }))
          }
        } catch {
          // Ignore parse errors
        }
      }

      es.onerror = () => {
        const wasAlreadyDone = eventSourceRef.current === null
        setState((prev) => ({
          ...prev,
          running: false,
          error: prev.done ? null : 'Connection lost',
        }))
        es.close()
        if (!wasAlreadyDone) {
          reject(new Error('Connection lost'))
        }
      }
    })
  }, [])

  const stop = useCallback(() => {
    eventSourceRef.current?.close()
    eventSourceRef.current = null
    setState((prev) => ({ ...prev, running: false }))
  }, [])

  return { ...state, run, stop }
}

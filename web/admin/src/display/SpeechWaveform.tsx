import { useEffect, useState, type RefObject } from 'react'

export default function SpeechWaveform({ voice, theme, colour = '#a56bff', emphasis = 150 }: { voice: string; theme: RefObject<HTMLIFrameElement | null>; colour?: string; emphasis?: number }) {
  const [audio, setAudio] = useState({ voice: '', level: 0 })
  useEffect(() => {
    const controller = new AbortController()
    const preference = window.matchMedia?.('(prefers-reduced-motion: reduce)')
    const listen = async () => {
      while (!controller.signal.aborted) {
        try {
          const response = await fetch('/api/voice-stream', { headers: { 'X-Jarvis-Display': '1' }, signal: controller.signal })
          if (!response.ok || !response.body) throw new Error('Voice visualiser stream unavailable')
          const reader = response.body.getReader(), decoder = new TextDecoder()
          let pending = ''
          try {
            while (!controller.signal.aborted) {
              const { value, done } = await reader.read()
              if (done) break
              pending += decoder.decode(value, { stream: true })
              const events = pending.split('\n\n'); pending = events.pop() || ''
              for (const event of events) {
                if (!event.startsWith('data: ')) continue
                const frame = JSON.parse(event.slice(6)) as { voice: string; level: number }
                const level = Math.min(1, Math.max(0, Number(frame.level) || 0))
                setAudio(old => ({ voice: frame.voice, level: preference?.matches ? 0.08 : Math.max(level, old.level * 0.72) }))
                theme.current?.contentWindow?.postMessage({ api_version: 1, type: 'voice', voice: frame.voice, level }, '*')
              }
            }
          } finally { await reader.cancel().catch(() => {}) }
        } catch { /* The static voice label remains available while reconnecting. */ }
        if (!controller.signal.aborted) await new Promise(resolve => setTimeout(resolve, 1000))
      }
    }
    void listen()
    return () => controller.abort()
  }, [theme])
  if ((audio.voice || voice) !== 'speaking') return null
  return <aside className="speech-waveform" aria-label="Jarvis speaking">
    <svg viewBox="0 0 240 240" role="img" aria-label="Circular waveform reacting to Jarvis’s voice">
      <defs>
        <radialGradient id="flare-colour" gradientUnits="userSpaceOnUse" cx="120" cy="120" r="120"><stop offset=".55" stopColor="#e6fcff" /><stop offset=".64" stopColor={colour} /><stop offset=".85" stopColor={colour} /><stop offset="1" stopColor={colour} stopOpacity=".15" /></radialGradient>
        <filter id="flare-glow" x="-20%" y="-20%" width="140%" height="140%"><feGaussianBlur stdDeviation="1.1" result="bloom" /><feMerge><feMergeNode in="bloom" /><feMergeNode in="SourceGraphic" /></feMerge></filter>
      </defs>
      <g data-flares="" fill="none" stroke="url(#flare-colour)" strokeWidth={0.45 * emphasis / 100} filter="url(#flare-glow)">{Array.from({ length: 8 }, (_, layer) => {
        const energy = Math.min(1, Math.sqrt(audio.level) * 1.8)
        // Standing lobes expand radially with speech; their angles never advance around the ring.
        const points = Array.from({ length: 256 }, (_, i) => {
          const angle = i * Math.PI / 128
          const lobes = Math.max(0, Math.sin(angle * 5 + 0.8 + layer * 0.035)) ** 3
          const uneven = 0.25 + 0.75 * (0.5 + 0.5 * Math.sin(angle * 3 + 2.4))
          const radius = 72 + energy * (layer + 1) / 8 * Math.min(40, (2 + 32 * lobes * uneven) * emphasis / 100)
          return `${(120 + Math.sin(angle) * radius).toFixed(2)} ${(120 - Math.cos(angle) * radius).toFixed(2)}`
        })
        return <path key={layer} opacity={0.3 + energy * 0.6} d={`M ${points.join(' L ')} Z`} />
      })}</g>
      <circle cx="120" cy="120" r="72" fill="var(--display-bg)" stroke="#d7f9ff" strokeWidth=".65" />
      <text x="120" y="117" textAnchor="middle" fill="var(--display-text)" fontSize="20">Jarvis</text><text x="120" y="139" textAnchor="middle" fill="var(--display-muted)" fontSize="13">Speaking</text>
    </svg>
  </aside>
}

import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Loader2, Play } from 'lucide-react'
import { getVoices, sampleVoice } from '@/api/tts'
import { buttonClass, inputClass } from '@/components/models/styles'
import { groupVoices, voiceInfo } from '@/components/setup/jobs'
import { errorMessage, errorStatus } from '@/lib/errors'

const voicesKey = ['tts', 'voices'] as const

/**
 * VoicePicker chooses the Kokoro voice and plays a short sample of it (AD3b). The sample needs
 * the voice model, so it waits for the Voice job to be ready; the choice itself can be made
 * at once and is saved when the step is confirmed.
 */
export default function VoicePicker({
  value,
  onChange,
  canSample,
}: {
  value: string
  onChange: (voice: string) => void
  canSample: boolean
}) {
  const voices = useQuery({ queryKey: voicesKey, queryFn: getVoices, staleTime: 60_000 })
  const [playing, setPlaying] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const url = useRef<string | null>(null)

  // Start from the voice in use.
  useEffect(() => {
    if (!value && voices.data) onChange(voices.data.current || voices.data.default)
  }, [value, voices.data, onChange])

  useEffect(
    () => () => {
      if (url.current) URL.revokeObjectURL(url.current)
    },
    [],
  )

  async function play() {
    setError(null)
    setPlaying(true)
    try {
      const blob = await sampleVoice(value)
      if (url.current) URL.revokeObjectURL(url.current)
      url.current = URL.createObjectURL(blob)
      const audio = new Audio(url.current)
      audio.onended = () => setPlaying(false)
      await audio.play()
    } catch (err) {
      setPlaying(false)
      setError(
        errorStatus(err) === 409 ? 'The voice model is still installing.' : errorMessage(err, 'Could not play the sample'),
      )
    }
  }

  if (voices.isError) return <p className="text-sm text-red-500">{errorMessage(voices.error, 'Could not list the voices')}</p>
  const groups = groupVoices(voices.data?.voices ?? [])

  return (
    <div className="space-y-2 rounded-lg border border-[var(--color-border)] p-3">
      <label htmlFor="voice-select" className="block text-sm font-medium text-[var(--color-text)]">
        Voice
      </label>
      <div className="flex flex-wrap items-center gap-2">
        <select
          id="voice-select"
          className={`${inputClass} max-w-xs`}
          value={value}
          disabled={!voices.data}
          onChange={(e) => onChange(e.target.value)}
        >
          {groups.map((g) => (
            <optgroup key={g.language} label={g.language}>
              {g.voices.map((v) => {
                const info = voiceInfo(v)
                return (
                  <option key={v} value={v}>
                    {info.name}
                    {info.gender ? ` (${info.gender})` : ''}
                    {v === voices.data?.default ? ' · default' : ''}
                  </option>
                )
              })}
            </optgroup>
          ))}
        </select>
        <button
          type="button"
          className={buttonClass.secondary}
          onClick={play}
          disabled={!canSample || !value || playing}
          title={canSample ? undefined : 'Available once the voice model is installed'}
        >
          {playing ? <Loader2 size={12} className="animate-spin" /> : <Play size={12} />} Play sample
        </button>
      </div>
      {!canSample && (
        <p className="text-xs text-[var(--color-text-muted)]">The sample plays once the voice model has downloaded.</p>
      )}
      {error && (
        <p role="alert" className="text-xs text-red-500">
          {error}
        </p>
      )}
      <p className="text-xs text-[var(--color-text-muted)]">
        The American and British English voices are the tested ones; the others may mispronounce words.
      </p>
    </div>
  )
}

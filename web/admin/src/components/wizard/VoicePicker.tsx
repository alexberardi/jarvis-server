import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Play } from 'lucide-react'
import { getVoices, sampleVoice } from '@/api/tts'
import { Alert, Button, Input, Select } from '@/components/ui'
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
      const { blob } = await sampleVoice(value)
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

  if (voices.isError)
    return (
      <Alert tone="danger" title="Couldn't list the voices">
        {errorMessage(voices.error, 'The server did not answer')}. Jarvis keeps its default voice; you can change it
        later on the Models page.
      </Alert>
    )
  const groups = groupVoices(voices.data?.voices ?? [])

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-end gap-2">
        {voices.data?.free_text ? (
          <Input
            id="voice-select"
            label="Voice"
            wrapperClassName="w-full max-w-xs"
            value={value}
            onChange={(e) => onChange(e.target.value)}
            hint="The provider lists no voices; type the voice's name or id."
          />
        ) : (
        <Select
          id="voice-select"
          label="Voice"
          wrapperClassName="w-full max-w-xs"
          value={value}
          disabled={!voices.data}
          onValueChange={onChange}
          groups={groups.map((g) => ({
            label: g.language,
            options: g.voices.map((v) => {
              const info = voiceInfo(v)
              return {
                value: v,
                label: `${info.name}${info.gender ? ` (${info.gender})` : ''}${v === voices.data?.default ? ' · default' : ''}`,
              }
            }),
          }))}
        />
        )}
        <Button
          onClick={play}
          disabled={!canSample || !value}
          loading={playing}
          icon={<Play size={16} aria-hidden />}
          title={canSample ? undefined : 'Available once the voice model is installed'}
        >
          Play sample
        </Button>
      </div>
      {!canSample && <p className="text-small text-text-muted">The sample plays once the voice model has downloaded.</p>}
      {error && (
        <p role="alert" className="text-small text-danger">
          {error}
        </p>
      )}
      <p className="text-small text-text-muted">
        The American and British English voices are the tested ones; the others may mispronounce words.
      </p>
    </div>
  )
}

import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getSpeechPresets, type SpeechPreset } from '@/api/speech'
import { fieldControl } from '@/components/ui/fieldStyles'

const presetsKey = ['speech', 'presets'] as const

/**
 * SpeechPresetPicker offers the remote speech providers (OpenAI, Groq, ElevenLabs, Other;
 * docs/llm/07-remote-speech.md §1). Picking one calls onPick with its defaults to fill the
 * form's URL/API/model fields, which stay editable — the preset itself is never stored, so the
 * select keeps its own local value and shows the picked preset's note.
 */
export default function SpeechPresetPicker({
  id,
  kind,
  onPick,
}: {
  id: string
  kind: 'stt' | 'tts'
  onPick: (preset: SpeechPreset) => void
}) {
  const presets = useQuery({ queryKey: presetsKey, queryFn: getSpeechPresets, staleTime: Infinity })
  const [picked, setPicked] = useState('')
  const list = presets.data?.[kind] ?? []
  const note = list.find((p) => p.id === picked)?.note

  return (
    <div className="min-w-0 space-y-1.5">
      <label htmlFor={id} className="block text-small font-medium text-text">
        Provider
      </label>
      <select
        id={id}
        value={picked}
        disabled={list.length === 0}
        onChange={(e) => {
          setPicked(e.target.value)
          const p = list.find((x) => x.id === e.target.value)
          if (p) onPick(p)
        }}
        className={fieldControl}
      >
        <option value="">{presets.isError ? "(couldn't load the presets)" : 'Choose a provider…'}</option>
        {list.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </select>
      <p className="text-small text-text-muted">{note ?? 'Picking one fills the fields below; they stay editable.'}</p>
    </div>
  )
}

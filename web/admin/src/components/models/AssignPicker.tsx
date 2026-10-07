import { LABEL_TITLE, labelsForKind, type Label } from '@/api/llm'

/** AssignPicker chooses which labels a new model is assigned to when its install finishes. */
export default function AssignPicker({
  kind,
  value,
  onChange,
  idPrefix,
}: {
  kind: string
  value: Label[]
  onChange: (labels: Label[]) => void
  idPrefix: string
}) {
  const options = labelsForKind(kind)
  if (options.length === 0) return null
  return (
    <fieldset className="flex flex-wrap items-center gap-x-4 gap-y-1">
      <legend className="sr-only">Use for</legend>
      <span className="text-xs text-[var(--color-text-muted)]">Use for:</span>
      {options.map((l) => {
        const id = `${idPrefix}-assign-${l}`
        return (
          <label key={l} htmlFor={id} className="flex items-center gap-1.5 text-xs text-[var(--color-text)]">
            <input
              id={id}
              type="checkbox"
              checked={value.includes(l)}
              onChange={(e) => onChange(e.target.checked ? [...value, l] : value.filter((x) => x !== l))}
            />
            {LABEL_TITLE[l]}
          </label>
        )
      })}
      {value.length === 0 && (
        <span className="text-xs text-[var(--color-text-muted)]">(install only; assign later)</span>
      )}
    </fieldset>
  )
}

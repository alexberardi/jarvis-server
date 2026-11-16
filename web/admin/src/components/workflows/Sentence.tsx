import type { Part } from '@/lib/workflows'

interface Props {
  parts: Part[]
  /** Given, a step's value ("Type **1500**") is a button that makes it change each run. */
  onValue?: (text: string) => void
}

/** Sentence renders a step or event sentence, its target in bold ("Click **Download CSV**"). */
export default function Sentence({ parts, onValue }: Props) {
  return parts.map((p, i) =>
    p.value && onValue ? (
      <button
        key={i}
        type="button"
        aria-label={`Make ${p.text} change each time`}
        title="Make this change each time"
        onClick={() => onValue(p.text)}
        className="inline-flex min-h-6 items-center rounded-control border border-dashed border-border-strong px-1.5 font-medium text-text transition-colors duration-(--duration-hover) hover:border-primary hover:text-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
      >
        {p.text}
      </button>
    ) : p.value && /^\{\w+\}$/.test(p.text) ? (
      // Already a value that changes each time.
      <span key={i} className="rounded-control bg-primary-soft px-1.5 font-medium text-primary">
        {p.text}
      </span>
    ) : p.bold ? (
      <strong key={i}>{p.text}</strong>
    ) : (
      <span key={i}>{p.text}</span>
    ),
  )
}

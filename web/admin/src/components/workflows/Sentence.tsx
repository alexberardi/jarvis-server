import type { Part } from '@/lib/workflows'

/** Sentence renders a step or event sentence, its target in bold ("Click **Download CSV**"). */
export default function Sentence({ parts }: { parts: Part[] }) {
  return parts.map((p, i) => (p.bold ? <strong key={i}>{p.text}</strong> : <span key={i}>{p.text}</span>))
}

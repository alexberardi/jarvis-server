import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'

/** CrossLink is the header link between Workflows and Recordings: a quiet, ghost-button-sized link. */
export default function CrossLink({ to, icon, children }: { to: string; icon: ReactNode; children: ReactNode }) {
  return (
    <Link
      to={to}
      className="inline-flex h-8 items-center gap-1.5 rounded-control px-3 text-small font-medium text-primary transition-colors duration-(--duration-hover) hover:bg-surface-alt"
    >
      {icon}
      {children}
    </Link>
  )
}

import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'

/** CrossLink is the header link between Workflows and Recordings: a quiet link, flush with the title on a phone. */
export default function CrossLink({ to, icon, children }: { to: string; icon: ReactNode; children: ReactNode }) {
  return (
    <Link
      to={to}
      className="inline-flex h-8 items-center gap-1.5 rounded-control text-small font-medium text-primary underline-offset-4 hover:underline"
    >
      {icon}
      {children}
    </Link>
  )
}

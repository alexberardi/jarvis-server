import { Link } from 'react-router-dom'
import { Compass } from 'lucide-react'
import { buttonStyles, EmptyState } from '@/components/ui'

/** NotFoundPage is any address the app has no page for, inside the shell, with the way back. */
export default function NotFoundPage() {
  return (
    <EmptyState
      className="min-h-[60vh] justify-center"
      icon={<Compass size={20} />}
      titleAs="h1"
      title="Page not found"
      description="Nothing lives at this address. The link may be mistyped, or the page has moved."
      action={
        <Link to="/" className={buttonStyles({ variant: 'primary' })}>
          Go home
        </Link>
      }
    />
  )
}

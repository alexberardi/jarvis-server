import { useId, type ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { Card, CardHeader } from '@/components/ui'

/**
 * ModelCard is one titled section of the Models page: a Card with an icon heading, a labelled
 * region named by its title. It replaces the old `Section` (components/models/ui) here; the
 * setup wizard still uses that one.
 */
export default function ModelCard({
  title,
  icon: Icon,
  description,
  actions,
  children,
  className,
  ...rest
}: {
  title: string
  icon?: LucideIcon
  description?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
  'data-testid'?: string
}) {
  const id = useId()
  return (
    <Card as="section" aria-labelledby={id} className={className} {...rest}>
      <CardHeader id={id} title={title} icon={Icon && <Icon size={18} />} description={description} actions={actions} />
      {children}
    </Card>
  )
}

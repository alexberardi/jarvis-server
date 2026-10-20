import { useState } from 'react'
import { Clock, Copy, KeyRound, RefreshCw, ShieldCheck, UserX, Users } from 'lucide-react'
import { toast } from 'sonner'
import { useAllUsers, useIssueTempPassword } from '@/hooks/useUsers'
import { Badge, Button, Card, EmptyState, ErrorState, Input, PageHeader, SkeletonList } from '@/components/ui'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'
import type { AdminUser, TempPasswordResult } from '@/api/admin'

function TempPasswordResultPanel({
  result,
  email,
  onDismiss,
}: {
  result: TempPasswordResult
  email: string
  onDismiss: () => void
}) {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(result.temp_password)
      toast.success('Temporary password copied')
    } catch {
      toast.error("Couldn't copy it. Select the password and copy it yourself.")
    }
  }

  return (
    <div role="status" className="rounded-card bg-warning-soft p-4">
      <p className="text-subheading text-text">Temporary password for {email}</p>
      <div className="mt-2 flex items-center gap-2">
        <code className="min-w-0 rounded-control border border-border bg-surface px-3 py-2 font-mono text-body tracking-wide break-all text-text">
          {result.temp_password}
        </code>
        <Button variant="ghost" size="icon" onClick={copy} aria-label="Copy the temporary password" title="Copy">
          <Copy size={16} aria-hidden />
        </Button>
      </div>
      <p className="mt-2 max-w-prose text-small text-text">
        Shown once and not stored anywhere. It works until {new Date(result.expires_at).toLocaleString()}. All of
        their sessions were signed out, and they'll be asked to set a new password when they next sign in.
      </p>
      <Button variant="primary" size="sm" className="mt-3" onClick={onDismiss}>
        Done
      </Button>
    </div>
  )
}

function ResetConfirm({
  user,
  onCancel,
  onConfirm,
  isPending,
}: {
  user: AdminUser
  onCancel: () => void
  onConfirm: (customPassword: string | null) => void
  isPending: boolean
}) {
  const [customPassword, setCustomPassword] = useState('')
  const tooShort = customPassword.length > 0 && customPassword.length < 8

  return (
    <div className="rounded-card bg-surface-alt p-4">
      <p className="max-w-prose text-body text-text">
        Reset the password for <span className="font-medium">{user.email}</span>? They're signed out everywhere and
        get a temporary password that they must change when they next sign in.
      </p>
      <Input
        wrapperClassName="mt-3 max-w-md"
        label="Temporary password (optional)"
        hint={tooShort ? undefined : 'Leave it blank to generate one.'}
        error={tooShort ? 'Use at least 8 characters' : undefined}
        value={customPassword}
        onChange={(e) => setCustomPassword(e.target.value)}
        placeholder="Custom temp password"
        autoComplete="off"
      />
      <div className="mt-3 flex flex-wrap gap-2">
        <Button variant="danger" loading={isPending} disabled={tooShort} onClick={() => onConfirm(customPassword || null)}>
          Reset password
        </Button>
        <Button variant="ghost" disabled={isPending} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  )
}

function UserRow({ user }: { user: AdminUser }) {
  const [panel, setPanel] = useState<'idle' | 'confirm'>('idle')
  const [result, setResult] = useState<TempPasswordResult | null>(null)
  const resetMutation = useIssueTempPassword()

  const handleConfirm = (customPassword: string | null) => {
    resetMutation.mutate(
      {
        userId: user.id,
        options: customPassword ? { temp_password: customPassword } : {},
      },
      {
        onSuccess: (data) => {
          setPanel('idle')
          setResult(data)
        },
        onError: (err) => toast.error(`Couldn't reset the password: ${errorMessage(err)}`),
      },
    )
  }

  const name = user.display_name || user.username
  return (
    <li className="px-4 py-4" data-testid={`user-${user.id}`}>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="min-w-0 text-subheading break-all text-text">{user.email}</span>
            {user.is_superuser && (
              <Badge tone="primary">
                <ShieldCheck size={12} aria-hidden />
                Superuser
              </Badge>
            )}
            {!user.is_active && (
              <Badge tone="danger">
                <UserX size={12} aria-hidden />
                Deactivated
              </Badge>
            )}
            {user.must_change_password && (
              <Badge tone="warning">
                <Clock size={12} aria-hidden />
                Temp password
              </Badge>
            )}
          </div>
          <dl className="mt-1 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 text-small text-text-muted">
            <dt>Name</dt>
            <dd className="text-text">
              {name}
              {user.display_name && user.display_name !== user.username && (
                <span className="text-text-muted"> ({user.username})</span>
              )}
            </dd>
            {user.households.length > 0 && (
              <>
                <dt>{user.households.length === 1 ? 'Household' : 'Households'}</dt>
                <dd className="text-text">{user.households.map((h) => `${h.household_name} (${h.role})`).join(', ')}</dd>
              </>
            )}
            <dt>Joined</dt>
            <dd className="text-text">{new Date(user.created_at).toLocaleDateString()}</dd>
          </dl>
        </div>

        {panel === 'idle' && !result && (
          <Button
            size="sm"
            icon={<KeyRound size={14} aria-hidden />}
            onClick={() => setPanel('confirm')}
            disabled={!user.is_active}
            title={user.is_active ? undefined : 'Reactivate the account first'}
            className="self-start"
          >
            Reset password
          </Button>
        )}
      </div>

      {panel === 'confirm' && (
        <div className="mt-3">
          <ResetConfirm
            user={user}
            onCancel={() => setPanel('idle')}
            onConfirm={handleConfirm}
            isPending={resetMutation.isPending}
          />
        </div>
      )}

      {result && (
        <div className="mt-3">
          <TempPasswordResultPanel result={result} email={user.email} onDismiss={() => setResult(null)} />
        </div>
      )}
    </li>
  )
}

export default function UsersPage() {
  const { data: users, isLoading, isError, error, refetch, isFetching } = useAllUsers()

  let body
  if (isLoading) {
    body = <SkeletonList rows={4} label="Loading users" />
  } else if (isError) {
    body = <ErrorState title="Couldn't load users" message={errorMessage(error)} onRetry={() => refetch()} />
  } else if (!users || users.length === 0) {
    body = (
      <EmptyState
        icon={<Users size={20} />}
        title="No users yet"
        description="People appear here once they have an account, from setup or an invitation."
      />
    )
  } else {
    body = (
      <ul aria-label="Users" className="divide-y divide-border">
        {users.map((user) => (
          <UserRow key={user.id} user={user} />
        ))}
      </ul>
    )
  }

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title="Users"
        description="Everyone with an account on this install. Reset a password here when someone can't sign in."
        actions={
          <Button variant="ghost" size="icon" aria-label="Refresh" title="Refresh" disabled={isFetching} onClick={() => refetch()}>
            <RefreshCw size={16} aria-hidden className={cn(isFetching && 'motion-safe:animate-spin')} />
          </Button>
        }
      />
      <Card flush>{body}</Card>
    </div>
  )
}

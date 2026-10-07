import { useState } from 'react'
import { RefreshCw, KeyRound, Copy, ShieldCheck, UserX, Clock } from 'lucide-react'
import { toast } from 'sonner'
import { useAllUsers, useIssueTempPassword } from '@/hooks/useUsers'
import { cn } from '@/lib/utils'
import type { AdminUser, TempPasswordResult } from '@/api/admin'

function Badge({ tone, children }: { tone: 'green' | 'red' | 'amber' | 'blue'; children: React.ReactNode }) {
  const tones = {
    green: 'bg-green-500/10 text-green-500',
    red: 'bg-red-500/10 text-red-500',
    amber: 'bg-amber-500/10 text-amber-500',
    blue: 'bg-blue-500/10 text-blue-500',
  }
  return (
    <span className={cn('inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium', tones[tone])}>
      {children}
    </span>
  )
}

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
      toast.error('Copy failed — select it manually')
    }
  }

  return (
    <div className="rounded-lg border border-amber-500/40 bg-amber-500/5 p-4">
      <p className="text-sm font-medium text-[var(--color-text)]">
        Temporary password for {email}
      </p>
      <div className="mt-2 flex items-center gap-2">
        <code className="rounded bg-[var(--color-surface-alt)] px-3 py-1.5 font-mono text-base tracking-wide text-[var(--color-text)]">
          {result.temp_password}
        </code>
        <button
          onClick={copy}
          className="rounded-lg p-1.5 hover:bg-[var(--color-surface-alt)]"
          title="Copy to clipboard"
        >
          <Copy size={14} className="text-[var(--color-text-muted)]" />
        </button>
      </div>
      <p className="mt-2 text-xs text-[var(--color-text-muted)]">
        Shown once — it is not stored anywhere. Valid until{' '}
        {new Date(result.expires_at).toLocaleString()}. All of the user's sessions were signed
        out; on their next login they'll be asked to set a new password.
      </p>
      <button
        onClick={onDismiss}
        className="mt-3 rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs font-medium text-white hover:opacity-90"
      >
        Done
      </button>
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
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] p-4">
      <p className="text-sm text-[var(--color-text)]">
        Reset the password for <span className="font-medium">{user.email}</span>? This signs the
        user out everywhere and replaces their password with a temporary one they must change at
        next login.
      </p>
      <div className="mt-3">
        <input
          type="text"
          value={customPassword}
          onChange={(e) => setCustomPassword(e.target.value)}
          placeholder="Custom temp password (optional — leave blank to generate)"
          className="w-full max-w-md rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
        />
        {tooShort && <p className="mt-1 text-xs text-red-500">Must be at least 8 characters</p>}
      </div>
      <div className="mt-3 flex gap-2">
        <button
          onClick={() => onConfirm(customPassword || null)}
          disabled={isPending || tooShort}
          className={cn(
            'rounded-lg px-3 py-1.5 text-xs font-medium transition-colors',
            isPending || tooShort
              ? 'cursor-not-allowed bg-[var(--color-surface)] text-[var(--color-text-muted)]'
              : 'bg-red-500 text-white hover:opacity-90',
          )}
        >
          {isPending ? (
            <span className="flex items-center gap-1.5">
              <RefreshCw size={12} className="animate-spin" />
              Resetting...
            </span>
          ) : (
            'Reset password'
          )}
        </button>
        <button
          onClick={onCancel}
          disabled={isPending}
          className="rounded-lg px-3 py-1.5 text-xs font-medium text-[var(--color-text-muted)] hover:bg-[var(--color-surface)]"
        >
          Cancel
        </button>
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
        onError: (err) => toast.error(`Reset failed: ${err.message}`),
      },
    )
  }

  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <span className="font-medium text-[var(--color-text)]">{user.email}</span>
            {user.is_superuser && (
              <Badge tone="blue">
                <ShieldCheck size={10} />
                Superuser
              </Badge>
            )}
            {!user.is_active && (
              <Badge tone="red">
                <UserX size={10} />
                Deactivated
              </Badge>
            )}
            {user.must_change_password && (
              <Badge tone="amber">
                <Clock size={10} />
                Temp password
              </Badge>
            )}
          </div>
          <div className="mt-1 space-y-0.5 text-xs text-[var(--color-text-muted)]">
            <p>
              <span className="font-medium">Username:</span> {user.username}
            </p>
            {user.households.length > 0 && (
              <p>
                <span className="font-medium">Households:</span>{' '}
                {user.households.map((h) => `${h.household_name} (${h.role})`).join(', ')}
              </p>
            )}
            <p>
              <span className="font-medium">Created:</span>{' '}
              {new Date(user.created_at).toLocaleDateString()}
            </p>
          </div>
        </div>

        {panel === 'idle' && !result && (
          <button
            onClick={() => setPanel('confirm')}
            disabled={!user.is_active}
            title={user.is_active ? undefined : 'Reactivate the account first'}
            className={cn(
              'flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-colors',
              user.is_active
                ? 'bg-[var(--color-surface-alt)] text-[var(--color-text)] hover:bg-[var(--color-primary)]/10 hover:text-[var(--color-primary)]'
                : 'cursor-not-allowed bg-[var(--color-surface-alt)] text-[var(--color-text-muted)]',
            )}
          >
            <KeyRound size={12} />
            Reset password
          </button>
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
          <TempPasswordResultPanel
            result={result}
            email={user.email}
            onDismiss={() => setResult(null)}
          />
        </div>
      )}
    </div>
  )
}

export default function UsersPage() {
  const { data: users, isLoading, isError, error, refetch, isFetching } = useAllUsers()

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <RefreshCw className="animate-spin text-[var(--color-primary)]" size={24} />
      </div>
    )
  }

  if (isError) {
    return (
      <div className="py-20 text-center">
        <p className="mb-2 text-red-500">Failed to load users</p>
        <p className="mb-4 text-sm text-[var(--color-text-muted)]">
          {(error as Error)?.message ?? 'Unknown error'}
        </p>
        <button
          onClick={() => refetch()}
          className="rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm text-white hover:opacity-90"
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold text-[var(--color-text)]">Users</h1>
        <button
          onClick={() => refetch()}
          disabled={isFetching}
          className={cn(
            'rounded-lg p-1.5 hover:bg-[var(--color-surface-alt)]',
            isFetching && 'animate-spin',
          )}
          title="Refresh"
        >
          <RefreshCw size={14} className="text-[var(--color-text-muted)]" />
        </button>
      </div>

      {users && users.length === 0 && (
        <div className="py-12 text-center">
          <p className="text-sm text-[var(--color-text-muted)]">No users found.</p>
        </div>
      )}

      <div className="space-y-3">
        {users?.map((user) => <UserRow key={user.id} user={user} />)}
      </div>
    </div>
  )
}

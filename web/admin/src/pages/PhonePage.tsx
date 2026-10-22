import { useState, type FormEvent, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Download, Phone, PhoneCall, PhoneIncoming, Play, Radio, Volume2 } from 'lucide-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  downloadSipTrace,
  getSipStatus,
  getSipTrace,
  startSipTest,
  type SipAccount,
  type SipInbound,
  type SipState,
  type SipTestName,
  type SipTestRun,
} from '@/api/phone'
import { Alert, Badge, Button, Card, CardHeader, EmptyState, ErrorState, Input, PageHeader, Skeleton, Spinner, toast, type BadgeTone } from '@/components/ui'
import { errorMessage } from '@/lib/errors'
import { formatServerTime, timeAgo } from '@/lib/time'

const sipStatusKey = ['phone', 'sip'] as const
const traceKey = ['phone', 'sip', 'trace'] as const

const stateBadge: Record<SipState, { label: string; tone: BadgeTone }> = {
  registered: { label: 'Registered', tone: 'success' },
  registering: { label: 'Registering', tone: 'neutral' },
  retrying: { label: 'Retrying', tone: 'warning' },
  stopped: { label: 'Stopped', tone: 'danger' },
  closed: { label: 'Shut down', tone: 'neutral' },
  not_started: { label: 'Not started', tone: 'danger' },
}

const tests: { name: SipTestName; title: string; icon: ReactNode; about: string }[] = [
  {
    name: 'connection',
    title: 'Connection',
    icon: <Radio size={16} aria-hidden />,
    about: 'Registers again and pings the PBX with OPTIONS.',
  },
  {
    name: 'echo',
    title: 'Echo test (*43)',
    icon: <Volume2 size={16} aria-hidden />,
    about:
      "Dials the PBX's echo test, plays a phrase after its instructions and transcribes what comes back: both audio directions and the round-trip delay. About 40 seconds.",
  },
  {
    name: 'digits',
    title: 'Hear the PBX (*65)',
    icon: <Phone size={16} aria-hidden />,
    about: "Dials *65, which says this extension's number, and checks that Jarvis hears it right.",
  },
  {
    name: 'call_me',
    title: 'Call my phone',
    icon: <PhoneCall size={16} aria-hidden />,
    about:
      'Sends you the usual confirm card for a call to your own phone. Answer it to check the caller ID and the dial format on your real trunk.',
  },
  {
    name: 'inbound',
    title: 'Inbound call',
    icon: <PhoneIncoming size={16} aria-hidden />,
    about:
      "Watches for 60 seconds while you call this extension, and shows the caller ID and the audio the PBX offers. Jarvis doesn't answer calls yet, so the PBX moves on to its next destination.",
  },
]

const runBadge: Record<SipTestRun['state'], { label: string; tone: BadgeTone }> = {
  running: { label: 'Running', tone: 'primary' },
  passed: { label: 'Passed', tone: 'success' },
  failed: { label: 'Failed', tone: 'danger' },
}

function anyRunning(accounts: SipAccount[] | undefined): boolean {
  return !!accounts?.some((a) => Object.values(a.tests).some((r) => r?.state === 'running'))
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-caption text-text-muted">{label}</dt>
      <dd className="text-small break-words text-text tabular-nums">{children}</dd>
    </div>
  )
}

const ms = (v: number | null | undefined) => (v == null ? '–' : `${v} ms`)

function inboundLine(i: SipInbound): string {
  const who = i.caller_name ? `${i.caller} (${i.caller_name})` : i.caller || 'unknown caller'
  return `${who} → ${i.to}, ${timeAgo(i.at)}`
}

function RunResult({ run }: { run: SipTestRun }) {
  const b = runBadge[run.state]
  const facts: [string, string][] = []
  if (run.register_rtt_ms != null) facts.push(['REGISTER', `${run.register_rtt_ms} ms`])
  if (run.options_rtt_ms != null) facts.push(['OPTIONS', `${run.options_rtt_ms} ms`])
  if (run.latency_ms != null) facts.push(['Echo delay', `${run.latency_ms} ms`])
  if (run.wer != null) facts.push(['Words wrong', `${Math.round(run.wer * 100)}%`])
  if (run.expected) facts.push(['Expected', run.expected])
  if (run.heard) facts.push(['Heard', `“${run.heard}”`])
  if (run.inbound) {
    facts.push(['Caller', inboundLine(run.inbound)])
    if (run.inbound.media) facts.push(['Audio offer', `${run.inbound.media} · ${run.inbound.codecs.join(', ')}`])
  }
  return (
    <div className="mt-3 space-y-2" aria-live="polite" data-testid={`run-${run.test}`}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={b.tone}>
          {run.state === 'running' && <Spinner />}
          {b.label}
        </Badge>
        <span className="text-caption text-text-muted">{formatServerTime(run.finished ?? run.started)}</span>
      </div>
      {run.state === 'running' && run.steps.length > 0 && <p className="text-small text-text-muted">{run.steps[run.steps.length - 1]}…</p>}
      {run.summary && <p className="text-small break-words text-text">{run.summary}</p>}
      {run.hint && (
        <Alert tone="warning" className="text-small">
          {run.hint}
        </Alert>
      )}
      {facts.length > 0 && (
        <dl className="grid gap-x-4 gap-y-2 sm:grid-cols-2">
          {facts.map(([k, v]) => (
            <Fact key={k} label={k}>
              {v}
            </Fact>
          ))}
        </dl>
      )}
      {run.state !== 'running' && run.steps.length > 0 && (
        <details>
          <summary className="cursor-pointer rounded-control text-caption text-text-muted">Steps</summary>
          <ol className="mt-1 list-decimal space-y-0.5 pl-5 text-caption text-text-muted">
            {run.steps.map((s, i) => (
              <li key={i}>{s}</li>
            ))}
          </ol>
        </details>
      )}
    </div>
  )
}

function TestRow({ account, test, busy }: { account: SipAccount; test: (typeof tests)[number]; busy: boolean }) {
  const qc = useQueryClient()
  const [number, setNumber] = useState('')
  const run = account.tests[test.name]
  const start = useMutation({
    mutationFn: () => startSipTest(account.local_port, test.name, test.name === 'call_me' ? { number } : {}),
    onSuccess: () => void qc.invalidateQueries({ queryKey: sipStatusKey }),
    onError: (err) => toast.error(errorMessage(err, `Couldn't start the ${test.title.toLowerCase()} test`)),
  })
  const submit = (e: FormEvent) => {
    e.preventDefault()
    start.mutate()
  }
  const label = run ? 'Run again' : 'Run'
  return (
    <li className="py-4 first:pt-0 last:pb-0">
      <form onSubmit={submit} className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <h3 className="flex items-center gap-2 text-subheading text-text">
            <span aria-hidden className="flex shrink-0 text-text-muted">
              {test.icon}
            </span>
            {test.title}
          </h3>
          <p className="mt-0.5 max-w-prose text-small text-text-muted">{test.about}</p>
          {test.name === 'call_me' && (
            <Input
              label="Your phone number"
              type="tel"
              autoComplete="tel"
              inputMode="tel"
              placeholder="(212) 555-0142"
              value={number}
              onChange={(e) => setNumber(e.target.value)}
              wrapperClassName="mt-3 max-w-xs"
              hint="US or Canada. The confirm card goes to your inbox."
            />
          )}
        </div>
        <Button
          type="submit"
          size="sm"
          className="self-start"
          icon={<Play size={14} aria-hidden />}
          loading={start.isPending}
          disabled={busy || (test.name === 'call_me' && number.trim() === '')}
          aria-label={`${label}: ${test.title}`}
        >
          {label}
        </Button>
      </form>
      {run && <RunResult run={run} />}
    </li>
  )
}

function AccountCard({ account }: { account: SipAccount }) {
  const b = stateBadge[account.state] ?? stateBadge.not_started
  const busy = Object.values(account.tests).some((r) => r?.state === 'running')
  const id = `sip-${account.local_port}`
  const owner = account.household_id ? `Household ${account.household_id}` : 'System account'
  const problem = account.state !== 'registered' && (account.last_error || account.hint)
  return (
    <Card as="section" aria-labelledby={id} className="space-y-5">
      <CardHeader
        id={id}
        title={`Extension ${account.extension} on ${account.server}:${account.port}`}
        description={`${owner} · ${account.transport.toUpperCase()} · local port ${account.local_port} · dials ${account.dial_prefix}${account.dial_format}`}
        actions={<Badge tone={b.tone}>{b.label}</Badge>}
        className="mb-0"
      />
      {problem && (
        <Alert tone={account.state === 'stopped' || account.state === 'not_started' ? 'danger' : 'warning'} title={account.last_error || undefined}>
          {account.hint}
        </Alert>
      )}
      <dl className="grid grid-cols-2 gap-x-4 gap-y-3 md:grid-cols-4">
        <Fact label="Since">{account.since ? timeAgo(account.since) : '–'}</Fact>
        <Fact label="OPTIONS round trip">{ms(account.options_rtt_ms)}</Fact>
        <Fact label="REGISTER round trip">{ms(account.register_rtt_ms)}</Fact>
        <Fact label="Active calls">{account.active_calls}</Fact>
        <Fact label="Attempts">{account.attempts}</Fact>
        <Fact label="Refusals in a row">{account.auth_failures}</Fact>
        <div className="col-span-2">
          <Fact label="Last inbound call">{account.last_inbound ? inboundLine(account.last_inbound) : 'None yet'}</Fact>
        </div>
      </dl>
      <div className="border-t border-border pt-4">
        <h3 className="mb-3 text-subheading text-text">Self-tests</h3>
        <ul className="divide-y divide-border">
          {tests.map((t) => (
            <TestRow key={t.name} account={account} test={t} busy={busy} />
          ))}
        </ul>
      </div>
    </Card>
  )
}

function TraceCard() {
  const { data, isLoading, isError, error, refetch } = useQuery({ queryKey: traceKey, queryFn: getSipTrace, refetchInterval: 15_000 })
  const [saving, setSaving] = useState(false)
  const calls = data?.filter((d) => d.kind === 'call') ?? []
  const save = async () => {
    setSaving(true)
    try {
      await downloadSipTrace()
    } catch (err) {
      toast.error(errorMessage(err, "Couldn't export the SIP trace"))
    } finally {
      setSaving(false)
    }
  }
  return (
    <Card as="section" aria-labelledby="sip-trace">
      <CardHeader
        id="sip-trace"
        title="SIP trace"
        description="The last 20 calls' SIP messages and the recent registrations, kept in memory. Passwords and digest responses are removed, and no audio is kept. Export it to send with a support request."
        actions={
          <Button size="sm" icon={<Download size={14} aria-hidden />} loading={saving} onClick={() => void save()}>
            Export
          </Button>
        }
      />
      {isLoading && <Skeleton className="h-10 w-full" />}
      {isError && <ErrorState title="Couldn't read the SIP trace" message={errorMessage(error)} onRetry={() => refetch()} />}
      {data &&
        (calls.length === 0 ? (
          <p className="text-small text-text-muted">
            No calls traced since Jarvis started{data.length > 0 ? `; ${data.length} registration exchanges are kept` : ''}.
          </p>
        ) : (
          <ul className="divide-y divide-border">
            {calls.slice(0, 10).map((d) => (
              <li key={d.call_id} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 py-2">
                <span className="min-w-0 font-mono text-caption break-all text-text">{d.messages[0]?.first_line ?? d.method}</span>
                <span className="shrink-0 text-caption text-text-muted tabular-nums">
                  {d.messages.length} messages · {timeAgo(d.started)}
                </span>
              </li>
            ))}
          </ul>
        ))}
    </Card>
  )
}

/**
 * PhonePage (docs/cc/phone-sip.md §6): Jarvis on a PBX as a SIP extension. Each account's
 * registration with plain hints, its self-tests, and the SIP trace export: what support needs
 * without seeing the user's PBX. It polls while a test runs.
 */
export default function PhonePage() {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: sipStatusKey,
    queryFn: getSipStatus,
    refetchInterval: (q) => (anyRunning(q.state.data?.accounts) ? 1000 : 10_000),
  })
  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        title="Phone"
        description="Jarvis on your phone system as a SIP extension: whether the PBX accepts it, self-tests for audio and calls, and a SIP trace for support."
      />
      {isLoading && (
        <Card role="status" aria-label="Loading the SIP status" className="space-y-3">
          <Skeleton className="h-6 w-1/2" />
          <Skeleton className="h-4 w-3/4" />
        </Card>
      )}
      {isError && (
        <Card>
          <ErrorState title="Couldn't read the SIP status" message={errorMessage(error)} onRetry={() => refetch()} />
        </Card>
      )}
      {data && (
        <div className="space-y-4">
          {data.accounts.length === 0 ? (
            <Card>
              <EmptyState
                icon={<Phone size={20} />}
                titleAs="h2"
                title="No SIP account is running"
                description="Calls go through Twilio unless phone.provider is sip. Set it, with the PBX's address and the extension's number and password (phone.sip.*), for the install or a household."
                action={
                  <Link to="/admin/settings" className="text-small font-medium text-primary underline-offset-2 hover:underline">
                    Open Settings
                  </Link>
                }
              />
            </Card>
          ) : (
            data.accounts.map((a) => <AccountCard key={a.local_port} account={a} />)
          )}
          <TraceCard />
        </div>
      )}
    </div>
  )
}

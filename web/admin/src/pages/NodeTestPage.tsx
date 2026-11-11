import { type FormEvent, type KeyboardEvent, type ReactNode, useEffect, useRef, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { ArrowLeft, FlaskConical, MessageCircle, RotateCw, SendHorizontal, ShieldAlert, Square } from 'lucide-react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import MessageBubble from '@/components/chat/MessageBubble'
import DeviceDiscovery from '@/components/devices/DeviceDiscovery'
import NodeSettings from '@/components/nodes/NodeSettings'
import { getDeviceConfig, listDeviceRooms } from '@/api/devices'
import { Badge, Banner, Button, Card, CardHeader, EmptyState, Input, PageHeader, buttonStyles } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import { normalizeShareCode, pollTestInstall, requestTestInstall, runTestAgents, sendToForge } from '@/api/testInstall'
import type { TestEvent, TestInstall, TestRun } from '@/api/testInstall'
import { useAuth } from '@/hooks/useAuth'
import { useChat } from '@/hooks/useChat'
import { useNodeLiveness, useNodesView } from '@/hooks/useNodes'
import { nodeLabel } from '@/lib/chatNode'
import { errorMessage } from '@/lib/errors'
import { nameOf } from '@/lib/session'
import { timeAgo } from '@/lib/time'
import { cn } from '@/lib/utils'

const LIVE = new Set(['pending', 'restarting'])

/** failed is a run whose command answered success: false. */
const failed = (run: TestRun) =>
  typeof run.output === 'object' && run.output !== null && (run.output as { success?: unknown }).success === false

/** outcome is what a run answered, in one line: its error when it failed, else its message or raw output. */
function outcome(run: TestRun): string {
  const o = run.output
  if (typeof o === 'object' && o !== null) {
    const r = o as { error?: unknown; message?: unknown }
    const text = failed(run) ? (r.error ?? r.message) : r.message
    if (typeof text === 'string') return text
  }
  return typeof o === 'string' ? o : JSON.stringify(o)
}

/**
 * NodeTestPage tries a Forge draft on one node: the share code, the install, then a chat through
 * that node to run the draft's commands by typing, the runs cc recorded, and Send to Forge. The
 * request id lives in the URL (?request=), so a reload comes back to the same test.
 */
export default function NodeTestPage() {
  const { id = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const requestId = params.get('request') ?? ''
  // The code is kept with the test: the Forge's Fix saves to the same draft, so Reinstall reuses it.
  const shareCode = params.get('code') ?? ''
  const started = (rid: string, code: string) => setParams({ request: rid, code })
  const liveness = useNodeLiveness()
  const node = liveness.data?.find((n) => n.node_id === id)
  // The registered name, as the node's card on the Nodes page shows it.
  const registered = useNodesView().data?.nodes.find((n) => n.node_id === id)?.name
  const name = registered || (node ? nodeLabel(node) : 'this node')

  const back = (
    <Link to="/admin/nodes" className={buttonStyles({ variant: 'ghost', size: 'sm' })}>
      <ArrowLeft size={16} aria-hidden="true" />
      Nodes
    </Link>
  )

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader title="Test a Forge command" description={`On ${name}`} actions={back} />
      {requestId ? (
        <TestRunView
          key={requestId}
          nodeId={id}
          householdId={node?.household_id ?? ''}
          tools={
            <NodeTools
              nodeId={id}
              householdId={node?.household_id ?? ''}
              name={name}
              online={node?.online === true}
              desktop={node?.node_kind === 'desktop'}
            />
          }
          requestId={requestId}
          shareCode={shareCode}
          onReinstalled={(rid) => started(rid, shareCode)}
          onRestart={() => setParams({}, { replace: true })}
        />
      ) : (
        <ShareCodeForm
          nodeId={id}
          name={name}
          offline={node !== undefined && !node.online}
          onStarted={started}
        />
      )}
    </div>
  )
}

function ShareCodeForm({
  nodeId,
  name,
  offline,
  onStarted,
}: {
  nodeId: string
  name: string
  offline: boolean
  onStarted: (requestId: string, code: string) => void
}) {
  const [code, setCode] = useState('')
  const start = useMutation({ mutationFn: () => requestTestInstall(nodeId, code), onSuccess: (rid) => onStarted(rid, code) })
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (code.length === 6 && !offline) start.mutate()
  }
  return (
    <Card className="max-w-xl">
      <form onSubmit={submit} className="space-y-4">
        <Input
          label="Share code"
          hint="The 6-character code on the Forge page."
          error={start.isError ? errorMessage(start.error, "Couldn't start the test install") : undefined}
          value={code}
          onChange={(e) => setCode(normalizeShareCode(e.target.value))}
          placeholder="AB3KM7"
          autoComplete="off"
          spellCheck={false}
          autoFocus
          className="font-mono tracking-[0.3em] uppercase"
        />
        <Banner tone="warning" icon={<ShieldAlert size={18} />} title="Unverified code">
          Pantry hasn't reviewed this draft. It runs on {name} with the same access as any installed command, for 20
          minutes. Only install codes you made or trust.
        </Banner>
        {offline && <p className="text-small text-danger">{name} is offline. Test installs need it online.</p>}
        <Button type="submit" variant="primary" disabled={code.length !== 6 || offline} loading={start.isPending}>
          Install on {name}
        </Button>
      </form>
    </Card>
  )
}

function TestRunView({
  nodeId,
  householdId,
  requestId,
  shareCode,
  onReinstalled,
  onRestart,
  tools,
}: {
  nodeId: string
  householdId: string
  requestId: string
  shareCode: string
  /** The node's settings and a device scan: shown for every draft, whatever it holds. */
  tools: ReactNode
  onReinstalled: (requestId: string) => void
  onRestart: () => void
}) {
  // One poll: the status while installing, then the runs every few seconds for the test window.
  const poll = useQuery({
    queryKey: ['test-install', nodeId, requestId],
    queryFn: () => pollTestInstall(nodeId, requestId),
    refetchInterval: (q) => (q.state.data?.status === 'completed' ? 5000 : q.state.data && !LIVE.has(q.state.data.status) ? false : 2000),
  })
  const t = poll.data
  const reinstall = useMutation({ mutationFn: () => requestTestInstall(nodeId, shareCode), onSuccess: onReinstalled })
  const again = (
    <>
      {shareCode && (
        <Button size="sm" variant="primary" icon={<RotateCw size={14} aria-hidden="true" />} loading={reinstall.isPending} onClick={() => reinstall.mutate()}>
          Reinstall
        </Button>
      )}
      <Button size="sm" onClick={onRestart}>
        Try another code
      </Button>
      {reinstall.isError && (
        <p role="alert" className="basis-full text-small text-danger">
          {errorMessage(reinstall.error, "Couldn't reinstall")}
        </p>
      )}
    </>
  )

  if (poll.isError) {
    return (
      <Banner role="alert" tone="danger" icon={<ShieldAlert size={18} />} title="Couldn't check the test install" actions={again}>
        {errorMessage(poll.error, 'The server did not answer.')}
      </Banner>
    )
  }
  if (!t || LIVE.has(t.status)) {
    return (
      <Card className="max-w-xl" role="status">
        <div className="flex items-center justify-between gap-3">
          <p className="min-w-0 truncate text-body text-text">{t?.package_name || 'The draft'}</p>
          <Badge dot>{t?.status === 'restarting' ? 'Restarting' : 'Installing'}</Badge>
        </div>
        <p className="mt-2 text-small text-text-muted">The node downloads and installs the draft. This takes a few seconds.</p>
      </Card>
    )
  }
  if (t.status !== 'completed') {
    return (
      <Banner role="alert" tone="danger" icon={<ShieldAlert size={18} />} title={`${t.package_name || 'The draft'} didn't install`} actions={again}>
        {t.error_message ?? 'The node reported a failure.'}
      </Banner>
    )
  }

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <TestChat
        nodeId={nodeId}
        householdId={householdId}
        commands={t.details?.commands ?? []}
        protocols={t.details?.protocols ?? []}
      />
      <div className="space-y-4">
        <InstallCard t={t} again={again} tools={tools} nodeId={nodeId} requestId={requestId} />
        <RunsCard runs={t.runs ?? []} />
        {(t.events?.length ?? 0) > 0 && <EventsCard events={t.events ?? []} />}
        <SendCard nodeId={nodeId} requestId={requestId} />
      </div>
    </div>
  )
}

function InstallCard({
  t,
  again,
  tools,
  nodeId,
  requestId,
}: {
  t: TestInstall
  again: ReactNode
  tools: ReactNode
  nodeId: string
  requestId: string
}) {
  const run = useMutation({ mutationFn: () => runTestAgents(nodeId, requestId) })
  const commands = t.details?.commands ?? []
  const protocols = t.details?.protocols ?? []
  const agents = t.details?.agents ?? []
  return (
    <Card>
      <CardHeader title={t.package_name} icon={<FlaskConical size={18} />} actions={<Badge tone="success">Installed</Badge>} className="mb-2" />
      <p className="text-small text-text-muted">
        {commands.length > 0 ? (
          <>
            Commands: <span className="text-text">{commands.join(', ')}</span>.{' '}
          </>
        ) : null}
        {protocols.length > 0 ? (
          <>
            Device protocols: <span className="text-text">{protocols.join(', ')}</span>.{' '}
          </>
        ) : null}
        {agents.length > 0 ? (
          <>
            Background agents: <span className="text-text">{agents.join(', ')}</span>. Each ran once at the install and
            then runs on its schedule; its runs are in Node events.{' '}
          </>
        ) : null}
        The node keeps it for 20 minutes from the install. After a fix in the Forge, Reinstall gets the new version.
      </p>
      <p className="mt-2 text-small text-text-muted">
        Its settings are the node&apos;s: set them, then Reinstall to run it with them. Devices it finds go when the test
        ends.
      </p>
      <div className="mt-3 flex flex-wrap gap-2">
        {agents.length > 0 && (
          <Button size="sm" variant="primary" loading={run.isPending} onClick={() => run.mutate()}>
            Run agents now
          </Button>
        )}
        {tools}
        {again}
        {run.isSuccess && !run.isPending && (
          <p role="status" className="basis-full text-small text-text-muted">
            Sent. Its run shows in Node events in a few seconds.
          </p>
        )}
        {run.isError && (
          <p role="alert" className="basis-full text-small text-danger">
            {errorMessage(run.error, "Couldn't run the agents")}
          </p>
        )}
      </div>
    </Card>
  )
}

/** NodeTools are the test node's settings and a device scan with it. */
function NodeTools({
  nodeId,
  householdId,
  name,
  online,
  desktop,
}: {
  nodeId: string
  householdId: string
  name: string
  online: boolean
  desktop: boolean
}) {
  const [scan, setScan] = useState(false)
  const config = useQuery({
    queryKey: ['devices', householdId, 'config'],
    queryFn: ({ signal }) => getDeviceConfig(householdId, signal),
    enabled: scan && !!householdId,
    retry: false,
  })
  const rooms = useQuery({
    queryKey: ['devices', householdId, 'rooms'],
    queryFn: ({ signal }) => listDeviceRooms(householdId, signal),
    enabled: scan && !!householdId,
    retry: false,
  })
  return (
    <>
      <NodeSettings nodeId={nodeId} name={name} online={online} desktop={desktop} />
      <Button size="sm" disabled={!online || !householdId} loading={scan && config.isPending} onClick={() => setScan(true)}>
        Scan for devices
      </Button>
      {scan && config.isError && (
        <p role="alert" className="basis-full text-small text-danger">
          {errorMessage(config.error, "Couldn't start a device scan")}
        </p>
      )}
      {scan && config.data && (
        <DeviceDiscovery
          household={householdId}
          config={config.data}
          rooms={rooms.data ?? []}
          roomsError={rooms.isError}
          nodeId={nodeId}
          onClose={() => setScan(false)}
          onChanged={() => {}}
          onExisting={() => setScan(false)}
        />
      )}
    </>
  )
}

function RunsCard({ runs }: { runs: TestRun[] }) {
  return (
    <Card>
      <CardHeader title="Runs" description="Each time Jarvis ran one of the draft's commands." className="mb-2" />
      {runs.length === 0 ? (
        <p className="text-small text-text-muted">No runs yet. Ask Jarvis to use the command.</p>
      ) : (
        <ul className="divide-y divide-border" aria-label="Runs">
          {runs.map((run, i) => (
            <li key={i} className="py-2" data-testid="test-run">
              <div className="flex items-center justify-between gap-2">
                <code className="min-w-0 truncate font-mono text-small text-text">{run.command}</code>
                <Badge tone={failed(run) ? 'danger' : 'success'}>{failed(run) ? 'Failed' : 'Ran'}</Badge>
              </div>
              <p className="mt-1 line-clamp-3 text-small break-words text-text-muted">{outcome(run)}</p>
              <p className="mt-0.5 text-small text-text-muted">{timeAgo(run.at)}</p>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}

/** eventLine is one node event in a line: a log line, a scan's result or a device control's. */
function eventLine(e: TestEvent): { text: string; bad: boolean } {
  if (e.kind === 'scan') {
    return e.error ? { text: `Scan (${e.protocol}): ${e.error}`, bad: true } : { text: `Scan (${e.protocol}): ${e.count ?? 0} found`, bad: false }
  }
  if (e.kind === 'agent') {
    if (e.ok) {
      const alerts = e.alert_count ? `, ${e.alert_count} alert${e.alert_count === 1 ? '' : 's'}` : ''
      return { text: `Agent ${e.agent}: ran in ${e.ms ?? 0} ms${alerts}`, bad: false }
    }
    // The error is a traceback: its last line says what went wrong (Send to Forge has it all).
    const last = (e.error ?? '').trim().split('\n').pop() || 'failed'
    return { text: `Agent ${e.agent}: ${last}`, bad: true }
  }
  if (e.kind === 'control') {
    const ok = e.success === true
    return { text: `${e.action} ${e.entity_id}: ${ok ? 'done' : (e.error ?? 'failed')}`, bad: !ok }
  }
  const bad = ['WARNING', 'ERROR', 'CRITICAL'].includes(e.level ?? '')
  return { text: `${e.level ?? 'LOG'} ${e.message ?? ''}`, bad }
}

/** EventsCard is what the test install did on the node besides chat: Send to Forge includes it. */
function EventsCard({ events }: { events: TestEvent[] }) {
  const newest = events.slice(-30).reverse()
  return (
    <Card>
      <CardHeader title="Node events" description="Its log lines, agent runs, device scans and controls. Send to Forge includes them." className="mb-2" />
      <ul className="max-h-72 divide-y divide-border overflow-y-auto" aria-label="Node events">
        {newest.map((e, i) => {
          const { text, bad } = eventLine(e)
          return (
            <li key={i} className={cn('py-1.5 font-mono text-small break-words', bad ? 'text-danger' : 'text-text-muted')}>
              {text}
            </li>
          )
        })}
      </ul>
    </Card>
  )
}

function SendCard({ nodeId, requestId }: { nodeId: string; requestId: string }) {
  const [note, setNote] = useState('')
  const send = useMutation({ mutationFn: () => sendToForge(nodeId, requestId, note), onSuccess: () => setNote('') })
  return (
    <Card>
      <CardHeader title="Send to Forge" description="The install result, every run and the node events, with your note, go back to the draft." className="mb-3" />
      <div className="space-y-2">
        <label htmlFor="forge-note" className="text-small font-medium text-text">
          Note (optional)
        </label>
        <textarea
          id="forge-note"
          rows={3}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="What went wrong, or what it should do instead"
          maxLength={2000}
          className={cn(fieldControl, 'h-auto py-2')}
        />
      </div>
      {send.isSuccess && (
        <p role="status" className="mt-2 text-small text-success">
          Sent. On the Forge page, use Fix with this report, then Reinstall here.
        </p>
      )}
      {send.isError && (
        <p role="alert" className="mt-2 text-small text-danger">
          {errorMessage(send.error, "Couldn't send to the Forge")}
        </p>
      )}
      <Button
        variant="primary"
        className="mt-3"
        icon={<SendHorizontal size={16} aria-hidden="true" />}
        loading={send.isPending}
        onClick={() => send.mutate()}
      >
        Send to Forge
      </Button>
    </Card>
  )
}

/** TestChat is a chat through the test node only; it mounts after the install, so its tools include the draft. */
function TestChat({
  nodeId,
  householdId,
  commands,
  protocols,
}: {
  nodeId: string
  householdId: string
  commands: string[]
  protocols: string[]
}) {
  const { state } = useAuth()
  const chat = useChat(state.user?.id ?? 0, householdId, nodeId)
  const [text, setText] = useState('')
  const listRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)

  const last = chat.messages[chat.messages.length - 1]
  useEffect(() => {
    const el = listRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [chat.messages.length, last?.text, last?.status])

  const submit = (e?: FormEvent) => {
    e?.preventDefault()
    if (chat.busy || !text.trim()) return
    chat.send(text)
    setText('')
    inputRef.current?.focus()
  }
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault()
      submit()
    }
  }

  return (
    <Card as="section" flush className="flex h-[32rem] min-h-0 flex-col lg:h-[calc(100dvh-12rem)]" aria-label="Chat">
      <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto p-4" aria-live="polite">
        {chat.messages.length === 0 ? (
          <div className="flex h-full items-center justify-center">
            <EmptyState
              icon={<MessageCircle size={20} />}
              title="Try the command"
              description={
                commands.length > 0
                  ? `Type what you'd say to Jarvis to use ${commands.join(' or ')}.`
                  : protocols.length > 0
                    ? `Once its devices are added, control them here, e.g. "turn on the plug".`
                    : "Type what you'd say to Jarvis to use the new command."
              }
            />
          </div>
        ) : (
          <div className="flex flex-col gap-4">
            {chat.messages.map((m, i) => (
              <MessageBubble
                key={m.id}
                message={m}
                userName={nameOf(state.user)}
                latest={i === chat.messages.length - 1}
                busy={chat.busy}
                onAnswer={(a) => chat.send(a)}
                onAction={(a) => chat.runAction(m.id, a)}
              />
            ))}
          </div>
        )}
      </div>
      <form onSubmit={submit} className="flex items-end gap-2 border-t border-border p-3">
        <label htmlFor="test-chat-input" className="sr-only">
          Message
        </label>
        <textarea
          id="test-chat-input"
          ref={inputRef}
          rows={1}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKey}
          placeholder="Ask Jarvis"
          maxLength={5000}
          className={cn(fieldControl, 'h-auto max-h-40 min-h-10 flex-1 resize-none py-2 [field-sizing:content]')}
        />
        {chat.busy ? (
          <Button size="icon" onClick={chat.stop} aria-label="Stop" title="Stop" icon={<Square size={16} aria-hidden="true" />} />
        ) : (
          <Button
            type="submit"
            variant={text.trim() ? 'primary' : 'secondary'}
            size="icon"
            aria-label="Send"
            title="Send"
            disabled={!text.trim()}
            icon={<SendHorizontal size={18} aria-hidden="true" />}
          />
        )}
      </form>
    </Card>
  )
}

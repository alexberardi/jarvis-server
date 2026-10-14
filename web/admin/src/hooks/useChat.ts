import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  getChatCapabilities,
  getNodeTools,
  getPrimaryNodeId,
  getThisDevice,
  isPasswordChange,
  listChatNodes,
  sendNodeAction,
  streamChat,
  warmupChat,
} from '@/api/chat'
import type { ChatAction, ChatImagePayload, NodeTools } from '@/api/chat'
import { notifyPasswordChangeRequired } from '@/api/client'
import type { NodeLiveness } from '@/api/nodes'
import { pickDefaultNode } from '@/lib/chatNode'
import { applyEvent, chatKey, emptyChat, endTurn, loadChat, saveChat } from '@/lib/chatState'
import type { ChatMessage, ChatState } from '@/lib/chatState'
import { errorMessage } from '@/lib/errors'
import { USER_LOCAL_PREFIX } from '@/lib/session'

/** How often the picker re-reads the nodes (webapp 01 §3.4: the offline banner clears by itself). */
export const NODE_POLL_MS = 30_000

const rememberedKey = (hh: string) => `${USER_LOCAL_PREFIX}chat-node:${hh}`
const toolsKey = (node: string) => `${USER_LOCAL_PREFIX}tools:${node}`

function readLocal(key: string): string {
  try {
    return localStorage.getItem(key) ?? ''
  } catch {
    return ''
  }
}

function writeLocal(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    // Not remembered: the chain picks again next time.
  }
}

function cachedTools(node: string): NodeTools | null {
  try {
    const raw = localStorage.getItem(toolsKey(node))
    return raw ? (JSON.parse(raw) as NodeTools) : null
  } catch {
    return null
  }
}

const newId = () => Date.now().toString(36) + Math.random().toString(36).slice(2, 8)
const timezone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'

export interface UseChat {
  nodes: NodeLiveness[]
  nodesLoading: boolean
  nodesError: string | null
  node: NodeLiveness | null
  /** This computer's agent (GET /api/addons/this-device), "" when the server can't tell. */
  thisDeviceId: string
  pickNode: (id: string) => void
  messages: ChatMessage[]
  busy: boolean
  /** The warmup is running (the input stays enabled: the first turn warms up anyway). */
  warming: boolean
  imagesEnabled: boolean
  maxImages: number
  maxImageBytes: number
  send: (text: string, images?: ChatImagePayload[]) => void
  newConversation: () => void
  stop: () => void
  runAction: (messageId: string, action: ChatAction) => Promise<void>
}

/**
 * useChat is the chat page's state (webapp 01 §3): the household's nodes and the default chain,
 * the per-tab conversation (sessionStorage), warmup with the node's tools, and turns streamed
 * through the event reducer.
 */
export function useChat(userId: number | string, householdId: string): UseChat {
  const key = chatKey(userId, householdId)
  const [chat, setChat] = useState<ChatState>(() => loadChat(key) ?? emptyChat())
  const [loadedKey, setLoadedKey] = useState(key)
  const [busy, setBusy] = useState(false)
  // The node+conversation a warmup last settled for; warming is "not yet for this one".
  const [settled, setSettled] = useState('')
  const abortRef = useRef<AbortController | null>(null)
  const toolsRef = useRef<NodeTools | null>(null)

  // Another household (or user) is another conversation: swap it in while rendering.
  if (loadedKey !== key) {
    setLoadedKey(key)
    setChat(loadChat(key) ?? emptyChat())
  }

  useEffect(() => {
    if (loadedKey === key) saveChat(key, chat)
  }, [key, loadedKey, chat])

  // Stop a running turn when the page goes away.
  useEffect(() => () => abortRef.current?.abort(), [])

  const nodesQ = useQuery({
    queryKey: ['chat-nodes', householdId],
    queryFn: () => listChatNodes(householdId),
    enabled: !!householdId,
    refetchInterval: NODE_POLL_MS,
    retry: false,
  })
  const primaryQ = useQuery({
    queryKey: ['chat-primary', householdId],
    queryFn: () => getPrimaryNodeId(householdId).catch(() => ''),
    enabled: !!householdId,
    staleTime: 5 * 60_000,
  })
  // Step 1 of the default chain: this computer's agent. The server matches the page's TCP peer
  // address with each agent's last heartbeat address (never a forwarded header); it is a hint
  // for the default only. "Can't tell" or a failure skips the step.
  const hereQ = useQuery({
    queryKey: ['this-device', householdId],
    queryFn: () => getThisDevice(householdId).catch(() => null),
    enabled: !!householdId,
    staleTime: 60_000,
  })
  const capsQ = useQuery({ queryKey: ['chat-capabilities'], queryFn: getChatCapabilities, staleTime: 60_000, retry: false })

  const nodes = useMemo(() => nodesQ.data ?? [], [nodesQ.data])
  const chainReady = nodesQ.isSuccess && !primaryQ.isLoading && !hereQ.isLoading
  const thisDeviceId = hereQ.data?.node_id ?? ''
  const node = nodes.find((n) => n.node_id === chat.nodeId) ?? null

  // The default chain, when the conversation has no node, or its node left the household.
  // Adjusted while rendering (not in an effect), like the household swap above.
  if (chainReady && loadedKey === key && !(chat.nodeId && nodes.some((n) => n.node_id === chat.nodeId))) {
    const { node: pick } = pickDefaultNode({
      nodes,
      primaryId: primaryQ.data ?? '',
      thisDeviceId,
      rememberedId: readLocal(rememberedKey(householdId)),
    })
    const id = pick?.node_id ?? ''
    if (id !== chat.nodeId) setChat(emptyChat(id))
  }

  // Tools, then warmup, whenever the node changes or a conversation starts. The tool list is
  // cached per node (as jarvis-web did) so a warmup never waits on a slow node.
  const toolsReady = useRef<Promise<void>>(Promise.resolve())
  useEffect(() => {
    const nodeId = chat.nodeId
    toolsRef.current = nodeId ? cachedTools(nodeId) : null
    if (!nodeId) return
    let cancelled = false
    toolsReady.current = getNodeTools(nodeId)
      .then((fresh) => {
        if (cancelled || (fresh.client_tools.length === 0 && fresh.available_commands.length === 0)) return
        toolsRef.current = fresh
        writeLocal(toolsKey(nodeId), JSON.stringify(fresh))
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [chat.nodeId])

  const warmFor = chat.conversationId ? '' : chat.nodeId
  useEffect(() => {
    if (!warmFor || !householdId) return
    let cancelled = false
    void (async () => {
      await toolsReady.current
      if (cancelled) return
      const tools = toolsRef.current
      try {
        const res = await warmupChat({
          node_id: warmFor,
          household_id: householdId,
          timezone: timezone(),
          ...(tools ? { client_tools: tools.client_tools, available_commands: tools.available_commands } : {}),
        })
        // Only adopt it if nothing started meanwhile (a turn hands back its own id).
        if (!cancelled) setChat((c) => (c.nodeId === warmFor && !c.conversationId ? { ...c, conversationId: res.conversation_id } : c))
      } catch (err) {
        if (isPasswordChange(err)) notifyPasswordChangeRequired()
      } finally {
        if (!cancelled) setSettled(warmFor)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [warmFor, householdId])
  const warming = !!warmFor && settled !== warmFor

  const send = useCallback(
    (text: string, images?: ChatImagePayload[]) => {
      const message = text.trim()
      if ((!message && !images?.length) || !chat.nodeId || busy) return
      const assistantId = newId()
      const user: ChatMessage = { id: newId(), role: 'user', text: message, images: images?.length || undefined }
      setChat((c) => ({
        ...c,
        messages: [...c.messages, user, { id: assistantId, role: 'assistant', text: '', pending: true }],
      }))
      setBusy(true)
      abortRef.current?.abort()
      const ctrl = new AbortController()
      abortRef.current = ctrl
      const tools = toolsRef.current
      // The tools ride along only when the conversation is new; a warm one already has them.
      const withTools = !chat.conversationId && !!tools
      streamChat(
        {
          message,
          node_id: chat.nodeId,
          household_id: householdId,
          ...(chat.conversationId ? { conversation_id: chat.conversationId } : {}),
          timezone: timezone(),
          include_reasoning: true,
          ...(withTools ? { client_tools: tools.client_tools, available_commands: tools.available_commands } : {}),
          ...(images?.length ? { images } : {}),
        },
        (ev) => setChat((c) => applyEvent(c, ev)),
        ctrl.signal,
      )
        .then(() => setChat((c) => endTurn(c)))
        .catch((err: unknown) => {
          if (err instanceof DOMException && err.name === 'AbortError') {
            setChat((c) => endTurn(c, 'Stopped.'))
            return
          }
          if (isPasswordChange(err)) notifyPasswordChangeRequired()
          setChat((c) => endTurn(c, errorMessage(err, 'Could not reach Jarvis')))
        })
        .finally(() => {
          if (abortRef.current === ctrl) abortRef.current = null
          setBusy(false)
        })
    },
    [chat.nodeId, chat.conversationId, householdId, busy],
  )

  const stop = useCallback(() => abortRef.current?.abort(), [])

  const newConversation = useCallback(() => {
    abortRef.current?.abort()
    setChat((c) => emptyChat(c.nodeId))
  }, [])

  const pickNode = useCallback(
    (id: string) => {
      writeLocal(rememberedKey(householdId), id)
      abortRef.current?.abort()
      // Another node is another conversation (its tools, its room).
      setChat((c) => (c.nodeId === id ? c : emptyChat(id)))
    },
    [householdId],
  )

  const runAction = useCallback(
    async (messageId: string, action: ChatAction) => {
      const msg = chat.messages.find((m) => m.id === messageId)
      if (!msg?.actionContext || !chat.nodeId) return
      let done: string
      try {
        const res = await sendNodeAction(chat.nodeId, {
          command_name: msg.actionContext.command_name,
          action_name: action.button_action,
          context: msg.actionContext.context ?? {},
        })
        if (res.status === 'timeout') done = 'The node did not answer in time.'
        else if (res.success === false) done = res.error || 'Action failed.'
        else done = action.completion_message ?? (action.button_action.includes('cancel') ? 'Cancelled.' : 'Done!')
      } catch (err) {
        done = errorMessage(err, 'Action failed.')
      }
      setChat((c) => ({ ...c, messages: c.messages.map((m) => (m.id === messageId ? { ...m, actionDone: done } : m)) }))
    },
    [chat.messages, chat.nodeId],
  )

  return {
    nodes,
    nodesLoading: nodesQ.isLoading,
    nodesError: nodesQ.isError ? errorMessage(nodesQ.error, 'Could not load your devices') : null,
    node,
    thisDeviceId,
    pickNode,
    messages: chat.messages,
    busy,
    warming,
    imagesEnabled: Boolean(capsQ.data?.images),
    maxImages: capsQ.data?.max_images ?? 4,
    maxImageBytes: capsQ.data?.max_image_bytes ?? 2 << 20,
    send,
    newConversation,
    stop,
    runAction,
  }
}

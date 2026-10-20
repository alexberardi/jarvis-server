import { useEffect, useRef, useState, type ChangeEvent } from 'react'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { completionKeymap } from '@codemirror/autocomplete'
import { Compartment, EditorState } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'
import { AlertTriangle, FileText } from 'lucide-react'
import { toast } from 'sonner'
import type { ModeSetting } from '@/api/llm'
import type { HouseholdPrompt, PromptVariable } from '@/api/prompts'
import { useAuth } from '@/hooks/useAuth'
import { useChatNodes } from '@/hooks/useChat'
import {
  useHouseholdPrompt,
  useImportPrompt,
  usePreviewPrompt,
  useResetPrompt,
  useSavePrompt,
  useSetPromptBase,
} from '@/hooks/usePrompt'
import { errorMessage } from '@/lib/errors'
import { promptTheme, promptVariables } from './promptEditor'
import { Badge, Banner, Button, ErrorState, Skeleton } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import { cn } from '@/lib/utils'
import ModelCard from './ModelCard'

const SOURCE_TEXT: Record<HouseholdPrompt['source'], string> = {
  builtin: 'built-in',
  import: 'imported',
  default: 'default',
}

const MODE_SOURCE_TEXT: Record<ModeSetting['source'], string> = {
  setting: 'set by the superuser',
  prompt: 'this prompt',
  template: 'chat template',
  remote: 'remote',
  default: 'default',
}

/** CodeEditor is the CodeMirror view: value in, edits out; the variables drive `{{` and the underline. */
function CodeEditor({
  value,
  onChange,
  variables,
}: {
  value: string
  onChange: (text: string) => void
  variables: PromptVariable[]
}) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const vars = useRef(new Compartment())
  const onChangeRef = useRef(onChange)
  useEffect(() => {
    onChangeRef.current = onChange
  })

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        extensions: [
          history(),
          keymap.of([...completionKeymap, ...defaultKeymap, ...historyKeymap]),
          EditorView.lineWrapping,
          EditorView.contentAttributes.of({ 'aria-label': 'Prompt text' }),
          promptTheme,
          vars.current.of([]),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) onChangeRef.current(u.state.doc.toString())
          }),
        ],
      }),
    })
    view.current = v
    return () => {
      v.destroy()
      view.current = null
    }
  }, [])

  // The text from outside (a load, reset, pick or import) replaces the editor's.
  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
    }
  }, [value])

  useEffect(() => {
    view.current?.dispatch({ effects: vars.current.reconfigure(promptVariables(variables)) })
  }, [variables])

  return <div ref={host} className="min-w-0" />
}

/** ModeSelect is one of the prompt's own mode values; "" follows the model. */
function ModeSelect({
  id,
  label,
  mode,
  value,
  options,
  fallback,
  onChange,
}: {
  id: string
  label: string
  mode: ModeSetting
  value: string
  options: string[]
  /** What the model side gives when nothing is detected. */
  fallback: string
  onChange: (v: string) => void
}) {
  // When this prompt's value is in use, the model's own is the detection.
  const fromModel = mode.source === 'prompt' ? mode.detected || fallback : mode.effective
  return (
    <div className="flex flex-wrap items-center gap-2">
      <label htmlFor={`prompt-${id}`} className="w-32 text-small font-medium text-text">
        {label}
      </label>
      <select
        id={`prompt-${id}`}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={cn(fieldControl, 'w-auto max-w-full sm:max-w-xs')}
      >
        <option value="">from the model ({fromModel})</option>
        {options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
      <code className="font-mono text-small text-text">{mode.effective}</code>
      <Badge tone={mode.source === 'setting' || mode.source === 'prompt' ? 'primary' : 'neutral'}>{MODE_SOURCE_TEXT[mode.source]}</Badge>
      {mode.source === 'setting' && value !== '' && (
        <span className="text-small text-text-muted">the superuser's value wins over this prompt's</span>
      )}
    </div>
  )
}

/**
 * PromptEditorCard edits the active household's voice prompt (P2 piece 10): the text with `{{`
 * completion and unknown variables underlined, its tool-calling and thinking values, reset,
 * a built-in or an imported file instead, and a preview for one of the household's nodes.
 */
export default function PromptEditorCard() {
  const { activeHouseholdId } = useAuth()
  const { data, isLoading, isError, error, refetch } = useHouseholdPrompt()
  const save = useSavePrompt()
  const reset = useResetPrompt()
  const setBase = useSetPromptBase()
  const imp = useImportPrompt()
  const preview = usePreviewPrompt()
  const nodes = useChatNodes(activeHouseholdId)

  const [loaded, setLoaded] = useState<HouseholdPrompt>()
  const [draft, setDraft] = useState('')
  const [toolCalling, setToolCalling] = useState('')
  const [thinking, setThinking] = useState('')
  const [builtin, setBuiltin] = useState('')
  const [nodeId, setNodeId] = useState('')
  const fileInput = useRef<HTMLInputElement>(null)

  // A new answer from the server (load, save, reset, pick, import) is the starting point again.
  if (data && data !== loaded) {
    setLoaded(data)
    setDraft(data.text)
    setToolCalling(data.prefs.tool_calling)
    setThinking(data.prefs.thinking_switch)
  }

  const nodeList = nodes.data ?? []
  const previewNode = nodeId || nodeList[0]?.node_id || ''
  const changed =
    !!data && (draft !== data.text || toolCalling !== data.prefs.tool_calling || thinking !== data.prefs.thinking_switch)
  const busy = save.isPending || reset.isPending || setBase.isPending || imp.isPending

  function edit(text: string) {
    setDraft(text)
    if (save.error) save.reset()
  }

  function onSave() {
    save.mutate(
      { text: draft, tool_calling: toolCalling, thinking_switch: thinking },
      { onSuccess: () => toast.success('Voice prompt saved') },
    )
  }

  function onReset() {
    if (!window.confirm('Drop your edits and go back to the original prompt?')) return
    reset.mutate(undefined, {
      onSuccess: () => toast.success('Voice prompt reset'),
      onError: (err) => toast.error(errorMessage(err, 'Could not reset the prompt')),
    })
  }

  function onUseBuiltin() {
    setBase.mutate(builtin, {
      onSuccess: (p) => {
        toast.success(`Voice prompt set to ${p.name}`)
        setBuiltin('')
      },
      onError: (err) => toast.error(errorMessage(err, 'Could not use that prompt')),
    })
  }

  async function onFile(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    const name = file.name.replace(/\.[^.]*$/, '') || file.name
    imp.mutate(
      { name, text: await file.text() },
      {
        onSuccess: (p) => toast.success(`Imported ${p.name}`),
        onError: (err) => toast.error(errorMessage(err, 'Could not import the file')),
      },
    )
  }

  return (
    <ModelCard
      title="Voice prompt"
      icon={FileText}
      description="The system prompt this household's voice turns use. Type {{ for the variables."
    >
      {isLoading && (
        <div role="status" aria-label="Loading the prompt" className="space-y-2">
          <Skeleton className="w-1/4" />
          <Skeleton className="h-40 w-full" />
        </div>
      )}
      {isError && (
        <ErrorState
          className="py-6"
          title="Couldn't read the prompt"
          message={errorMessage(error, 'The server did not answer.')}
          onRetry={() => refetch()}
        />
      )}
      {data && (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <code className="font-mono text-body text-text">{data.name || '(none)'}</code>
            <Badge tone={data.source === 'default' ? 'neutral' : 'primary'}>{SOURCE_TEXT[data.source]}</Badge>
            {data.edited && <Badge tone="warning">edited</Badge>}
          </div>

          {data.fallback && (
            <Banner role="alert" tone="warning" icon={<AlertTriangle size={16} />}>
              {data.fallback}
            </Banner>
          )}

          <CodeEditor value={draft} onChange={edit} variables={data.variables} />

          <div className="space-y-3">
            <ModeSelect
              id="tool_calling"
              label="Tool calling"
              mode={data.tool_calling}
              value={toolCalling}
              options={['native', 'text']}
              fallback="text"
              onChange={setToolCalling}
            />
            <ModeSelect
              id="thinking_switch"
              label="Thinking switch"
              mode={data.thinking_switch}
              value={thinking}
              options={['suffix', 'none']}
              fallback="none"
              onChange={setThinking}
            />
          </div>

          {save.error && (
            <Banner role="alert" tone="danger" icon={<AlertTriangle size={16} />}>
              {errorMessage(save.error, 'Could not save the prompt')}
            </Banner>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <Button variant="primary" disabled={!changed || busy} onClick={onSave}>
              Save
            </Button>
            {data.edited && (
              <Button disabled={busy} onClick={onReset}>
                Reset to original
              </Button>
            )}
          </div>

          <div className="flex flex-wrap items-center gap-2 border-t border-border pt-4">
            <label htmlFor="prompt-builtin" className="sr-only">
              Built-in prompt
            </label>
            <select
              id="prompt-builtin"
              value={builtin}
              onChange={(e) => setBuiltin(e.target.value)}
              className={cn(fieldControl, 'w-auto max-w-full sm:max-w-xs')}
            >
              <option value="" disabled>
                Built-in prompt…
              </option>
              {data.builtins.map((b) => (
                <option key={b} value={b}>
                  {b}
                </option>
              ))}
            </select>
            <Button disabled={!builtin || busy} onClick={onUseBuiltin}>
              Use
            </Button>
            <Button disabled={busy} onClick={() => fileInput.current?.click()}>
              Import file
            </Button>
            <input
              ref={fileInput}
              type="file"
              accept=".txt,.prompt,text/plain"
              aria-label="Prompt file"
              className="hidden"
              onChange={(e) => void onFile(e)}
            />
          </div>

          <div className="space-y-3 border-t border-border pt-4">
            <div className="flex flex-wrap items-center gap-2">
              <label htmlFor="prompt-preview-node" className="text-small font-medium text-text">
                Preview node
              </label>
              <select
                id="prompt-preview-node"
                value={previewNode}
                onChange={(e) => setNodeId(e.target.value)}
                className={cn(fieldControl, 'w-auto max-w-full sm:max-w-xs')}
                disabled={nodeList.length === 0}
              >
                {nodeList.length === 0 && <option value="">No nodes</option>}
                {nodeList.map((n) => (
                  <option key={n.node_id} value={n.node_id}>
                    {n.room ? `${n.room} · ${n.node_id.slice(0, 8)}` : n.node_id}
                  </option>
                ))}
              </select>
              <Button
                disabled={!previewNode}
                loading={preview.isPending}
                onClick={() => preview.mutate({ text: draft, nodeId: previewNode })}
              >
                Preview
              </Button>
            </div>
            {preview.error && (
              <p role="alert" className="text-small text-danger">
                {errorMessage(preview.error, 'Could not preview the prompt')}
              </p>
            )}
            {preview.data && (
              <div className="space-y-2">
                <div className="flex flex-wrap gap-x-4 gap-y-1 text-small text-text-muted">
                  <span>
                    Tool calling: <code className="font-mono text-text">{preview.data.tool_calling}</code>
                  </span>
                  <span>
                    Tools sent via the API: <code className="font-mono text-text">{preview.data.tools_via_api}</code>
                  </span>
                  <span>
                    User suffix:{' '}
                    <code className="font-mono text-text">{preview.data.user_suffix ? preview.data.user_suffix : '(none)'}</code>
                  </span>
                </div>
                {preview.data.partial && (
                  <p className="text-small text-warning">
                    No warmup from this node yet: rendered without its tools, devices or command flags.
                  </p>
                )}
                <pre className="max-h-96 overflow-auto rounded-control bg-surface-alt p-3 font-mono text-small break-words whitespace-pre-wrap text-text">
                  {preview.data.system_prompt}
                </pre>
              </div>
            )}
          </div>
        </div>
      )}
    </ModelCard>
  )
}

import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { EditorView } from '@codemirror/view'
import { startCompletion } from '@codemirror/autocomplete'
import { AxiosError, AxiosHeaders } from 'axios'
import * as chatApi from '@/api/chat'
import * as promptsApi from '@/api/prompts'
import PromptEditorCard from './PromptEditorCard'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ activeHouseholdId: 'hh1' }) }))
vi.mock('@/api/chat', async (orig) => ({ ...(await orig<typeof import('@/api/chat')>()), listChatNodes: vi.fn() }))
vi.mock('@/api/prompts', async (orig) => ({
  ...(await orig<typeof import('@/api/prompts')>()),
  getHouseholdPrompt: vi.fn(),
  savePrompt: vi.fn(),
  resetPrompt: vi.fn(),
  setPromptBase: vi.fn(),
  importPrompt: vi.fn(),
  previewPrompt: vi.fn(),
}))

const api = vi.mocked(promptsApi)
const chat = vi.mocked(chatApi)

const prompt: promptsApi.HouseholdPrompt = {
  source: 'builtin',
  name: 'qwen3',
  text: 'You are Jarvis in the {{room}}.',
  original: 'You are Jarvis in the {{room}}.',
  edited: false,
  fallback: '',
  builtins: ['qwen3', 'chatgpt'],
  variables: [
    { name: 'room', kind: 'inline', description: 'The room the node is in' },
    { name: 'persona', kind: 'section', description: "The household's persona" },
  ],
  prefs: { tool_calling: '', thinking_switch: '' },
  tool_calling: { value: 'auto', detected: 'native', effective: 'native', source: 'template' },
  thinking_switch: { value: 'auto', detected: 'none', effective: 'none', source: 'default' },
}

// jsdom has no layout: CodeMirror measures ranges for tooltips and cursors.
beforeAll(() => {
  const rect = { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, toJSON: () => ({}) }
  Range.prototype.getClientRects = () => ({ length: 0, item: () => null, [Symbol.iterator]: [][Symbol.iterator] }) as unknown as DOMRectList
  Range.prototype.getBoundingClientRect = () => rect as DOMRect
})

beforeEach(() => {
  vi.clearAllMocks()
  api.getHouseholdPrompt.mockResolvedValue(prompt)
  chat.listChatNodes.mockResolvedValue([
    { node_id: 'n1', room: 'kitchen', household_id: 'hh1', online: true, last_seen: null, last_seen_version: null, is_busy: false },
  ])
})

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <PromptEditorCard />
    </QueryClientProvider>,
  )
}

/** editor waits for the CodeMirror view and returns it. */
async function editor(): Promise<EditorView> {
  const el = await waitFor(() => {
    const e = document.querySelector('.cm-editor')
    if (!e) throw new Error('no editor')
    return e as HTMLElement
  })
  const view = EditorView.findFromDOM(el)
  if (!view) throw new Error('no view')
  return view
}

function setText(view: EditorView, text: string) {
  act(() => {
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, selection: { anchor: text.length } })
  })
}

function badRequest(detail: string) {
  const res = { status: 400, statusText: 'Bad Request', data: { detail }, headers: {}, config: { headers: new AxiosHeaders() } }
  return new AxiosError('Request failed', 'ERR_BAD_REQUEST', undefined, undefined, res)
}

describe('PromptEditorCard', () => {
  it('loads and shows the text', async () => {
    renderCard()
    const view = await editor()
    await waitFor(() => expect(view.state.doc.toString()).toBe(prompt.text))
    expect(screen.getByText('qwen3', { selector: 'code' })).toBeInTheDocument()
    expect(screen.getByText('built-in')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('Save sends PUT with the edited text', async () => {
    api.savePrompt.mockResolvedValue({ ...prompt, text: 'Hi {{room}}', edited: true })
    renderCard()
    const view = await editor()
    await waitFor(() => expect(view.state.doc.toString()).toBe(prompt.text))
    setText(view, 'Hi {{room}}')
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.savePrompt).toHaveBeenCalledWith('hh1', { text: 'Hi {{room}}', tool_calling: '', thinking_switch: '' }),
    )
  })

  it("shows a 400's detail", async () => {
    api.savePrompt.mockRejectedValue(badRequest('line 1: unknown variable {{nope}}'))
    renderCard()
    const view = await editor()
    await waitFor(() => expect(view.state.doc.toString()).toBe(prompt.text))
    setText(view, 'Hi {{nope}}')
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('line 1: unknown variable {{nope}}')).toBeInTheDocument()
  })

  it('sends tool_calling: "native" when chosen', async () => {
    api.savePrompt.mockResolvedValue(prompt)
    renderCard()
    await editor()
    const select = await screen.findByLabelText('Tool calling')
    expect(select).toHaveDisplayValue('from the model (native)')
    fireEvent.change(select, { target: { value: 'native' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.savePrompt).toHaveBeenCalledWith('hh1', { text: prompt.text, tool_calling: 'native', thinking_switch: '' }),
    )
  })

  it('Reset calls /reset after confirm', async () => {
    api.resetPrompt.mockResolvedValue(prompt)
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    api.getHouseholdPrompt.mockResolvedValue({ ...prompt, text: 'edited', edited: true })
    renderCard()
    await editor()
    fireEvent.click(await screen.findByRole('button', { name: 'Reset to original' }))
    expect(confirm).toHaveBeenCalled()
    await waitFor(() => expect(api.resetPrompt).toHaveBeenCalledWith('hh1'))
    confirm.mockRestore()
  })

  it('offers the variables after {{ and inserts the one picked', async () => {
    renderCard()
    const view = await editor()
    await waitFor(() => expect(view.state.doc.toString()).toBe(prompt.text))
    setText(view, 'Hello {{')
    act(() => {
      startCompletion(view)
    })
    const option = await waitFor(() => {
      const o = [...document.querySelectorAll('.cm-tooltip-autocomplete li')].find((li) => li.textContent?.includes('room'))
      if (!o) throw new Error('no room option')
      return o as HTMLElement
    })
    expect(option.textContent).toContain('The room the node is in')
    expect(document.querySelector('.cm-tooltip-autocomplete')?.textContent).toContain('persona')
    fireEvent.mouseDown(option)
    await waitFor(() => expect(view.state.doc.toString()).toBe('Hello {{room}}'))
  })

  it('underlines an unknown variable', async () => {
    renderCard()
    const view = await editor()
    await waitFor(() => expect(view.state.doc.toString()).toBe(prompt.text))
    setText(view, 'In the {{room}} with {{nope}}')
    await waitFor(() => {
      const marked = [...view.contentDOM.querySelectorAll('.decoration-wavy')]
      expect(marked.map((m) => m.textContent)).toEqual(['{{nope}}'])
      expect(marked[0]).toHaveClass('underline', 'decoration-red-500')
    })
  })

  it('previews the draft for a node and notes a partial render', async () => {
    api.previewPrompt.mockResolvedValue({
      system_prompt: 'You are Jarvis in the kitchen.',
      tool_calling: 'native',
      tools_via_api: 4,
      user_suffix: ' /no_think',
      partial: true,
    })
    renderCard()
    await editor()
    const node = await screen.findByLabelText('Preview node')
    await waitFor(() => expect(node).toHaveDisplayValue(/kitchen/))
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }))
    await waitFor(() => expect(api.previewPrompt).toHaveBeenCalledWith('hh1', prompt.text, 'n1'))
    expect(await screen.findByText('You are Jarvis in the kitchen.')).toBeInTheDocument()
    expect(screen.getByText(/no warmup from this node yet/i)).toBeInTheDocument()
    expect(screen.getByText('/no_think', { exact: false })).toBeInTheDocument()
  })
})

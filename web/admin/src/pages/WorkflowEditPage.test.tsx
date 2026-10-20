import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { TaughtWorkflow, WorkflowPut } from '@/api/workflows'
import WorkflowEditPage from './WorkflowEditPage'

const api = vi.hoisted(() => ({ getWorkflow: vi.fn(), saveWorkflow: vi.fn() }))
vi.mock('@/api/workflows', async (orig) => ({ ...(await orig<typeof import('@/api/workflows')>()), ...api }))

const bank = (over: Partial<TaughtWorkflow> = {}): TaughtWorkflow => ({
  id: 'tw_1',
  household_id: 'h1',
  owner_id: 7,
  name: 'Download bank statement',
  slug: 'download-bank-statement',
  triggers: ['get my bank statement'],
  params: [{ name: 'month', kind: 'month', example: '2026-09', description: 'the statement month' }],
  steps: [
    { intent: 'Open the bank', target: 'browser', site: 'bank.example.com', action: { kind: 'navigate', intent: 'Open the bank', url: 'https://bank.example.com/login' } },
    { intent: 'Go to statements', target: 'browser', action: { kind: 'press', intent: 'Go to statements', ref: 'e3' }, element: { role: 'link', name: 'Statements' } },
    { intent: 'Enter the month {month}', target: 'browser', action: { kind: 'type', intent: 'Enter the month {month}', text: '{month}' }, element: { role: 'textbox', name: 'Statement month' } },
    { intent: 'Pick the checking account', target: 'browser', action: { kind: 'select', intent: 'Pick the checking account', text: 'checking', ref: 'e5' }, element: { role: 'combobox', name: 'Account' } },
    { intent: 'Download the CSV', target: 'browser', action: { kind: 'press', intent: 'Download the CSV', ref: 'e9' }, element: { role: 'button', name: 'Download CSV' }, risk: 'sharing' },
  ],
  success: { download: { name_like: 'statement*.csv' } },
  shared: false,
  version: 1,
  created_at: '2026-10-19T10:00:00.000Z',
  updated_at: '2026-10-19T10:00:00.000Z',
  editable: true,
  ...over,
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/workflows/tw_1']}>
        <Routes>
          <Route path="/workflows/:id" element={<WorkflowEditPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const steps = () => screen.getAllByTestId('step')
const sentence = (i: number) => steps()[i].querySelector('p')!.textContent

async function save(): Promise<WorkflowPut> {
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  })
  expect(api.saveWorkflow).toHaveBeenCalledTimes(1)
  expect(api.saveWorkflow.mock.calls[0][0]).toBe('tw_1')
  return api.saveWorkflow.mock.calls[0][1] as WorkflowPut
}

beforeEach(() => {
  vi.clearAllMocks()
  api.getWorkflow.mockResolvedValue(bank())
  api.saveWorkflow.mockImplementation(async (_id: string, body: WorkflowPut) => ({ ...bank(), ...body, version: 2, updated_at: 'x' }))
})

describe('WorkflowEditPage', () => {
  it('says what failed and loads again on Try again', async () => {
    api.getWorkflow.mockRejectedValueOnce(new Error('boom'))
    renderPage()
    expect(await screen.findByText("Couldn't load this workflow")).toBeInTheDocument()
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    })
    expect(await screen.findAllByTestId('step')).toHaveLength(5)
  })

  it('shows the steps as sentences, the risky one asking first', async () => {
    renderPage()
    expect(await screen.findByRole('heading', { name: 'Download bank statement' })).toBeInTheDocument()
    expect(steps()).toHaveLength(5)
    expect(sentence(0)).toBe('Open bank.example.com')
    expect(sentence(1)).toBe('Click Statements')
    expect(within(steps()[1]).getByText('Statements').tagName).toBe('STRONG')
    expect(sentence(2)).toBe('Type {month}')
    expect(sentence(3)).toBe('Choose checking')
    expect(within(steps()[4]).getByText('Asks first')).toBeInTheDocument()
    // A step that was risky when taught always asks; another can be made to.
    expect(screen.getByRole('switch', { name: 'Ask first before step 5' })).toBeChecked()
    expect(screen.getByRole('switch', { name: 'Ask first before step 5' })).toBeDisabled()
    expect(screen.getByRole('switch', { name: 'Ask first before step 1' })).not.toBeChecked()
    expect(screen.getByTestId('success-sentence')).toHaveTextContent('a file named like statement*.csv downloads.')
  })

  it('saves exactly the fields PUT takes, unchanged when nothing was edited', async () => {
    renderPage()
    await screen.findAllByTestId('step')
    const body = await save()
    expect(Object.keys(body).sort()).toEqual(['name', 'params', 'shared', 'steps', 'success', 'triggers'])
    const wf = bank()
    expect(body).toEqual({
      name: wf.name,
      triggers: wf.triggers,
      params: wf.params,
      steps: wf.steps,
      success: wf.success,
      shared: false,
    })
  })

  it('params: a value turned off goes back to its example, one turned on is named in its step', async () => {
    renderPage()
    await screen.findAllByTestId('step')
    const section = screen.getByRole('region', { name: 'Which of these change each time?' })
    const month = within(section).getByRole('switch', { name: '2026-09 changes each time' })
    const account = within(section).getByRole('switch', { name: 'checking changes each time' })
    expect(month).toBeChecked()
    expect(account).not.toBeChecked()

    fireEvent.click(month)
    expect(sentence(2)).toBe('Type 2026-09')
    fireEvent.click(account)
    fireEvent.change(within(section).getByLabelText('Name for checking'), { target: { value: 'which_account' } })
    expect(sentence(3)).toBe('Choose {which_account}')

    const body = await save()
    expect(body.params).toEqual([{ name: 'which_account', kind: 'text', example: 'checking' }])
    expect(body.steps[2].action.text).toBe('2026-09')
    expect(body.steps[2].intent).toBe('Enter the month 2026-09')
    expect(body.steps[2].action.intent).toBe('Enter the month 2026-09')
    expect(body.steps[3].action.text).toBe('{which_account}')
    expect(body.steps[3].action.ref).toBe('e5')
  })

  it('params: renaming one renames it in the steps; a bad or repeated name blocks Save', async () => {
    renderPage()
    await screen.findAllByTestId('step')
    const section = screen.getByRole('region', { name: 'Which of these change each time?' })
    fireEvent.change(within(section).getByLabelText('Name for 2026-09'), { target: { value: 'period' } })
    expect(sentence(2)).toBe('Type {period}')

    fireEvent.click(within(section).getByRole('switch', { name: 'checking changes each time' }))
    fireEvent.change(within(section).getByLabelText('Name for checking'), { target: { value: 'period' } })
    expect(screen.getByRole('alert')).toHaveTextContent('Two values are named "period".')
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.change(within(section).getByLabelText('Name for checking'), { target: { value: 'acct' } })

    const body = await save()
    expect(body.params.map((p) => p.name)).toEqual(['period', 'acct'])
    expect(body.params[0]).toEqual({ name: 'period', kind: 'month', example: '2026-09', description: 'the statement month' })
    expect(body.steps[2].action.text).toBe('{period}')
    expect(body.steps[2].intent).toBe('Enter the month {period}')
  })

  it('reorders by the buttons and by dragging', async () => {
    renderPage()
    await screen.findAllByTestId('step')
    fireEvent.click(screen.getByRole('button', { name: 'Move step 2 down' }))
    expect(sentence(1)).toBe('Type {month}')
    expect(sentence(2)).toBe('Click Statements')
    // Drag the last step onto the first.
    fireEvent.dragStart(steps()[4])
    fireEvent.dragOver(steps()[0])
    fireEvent.drop(steps()[0])
    expect(sentence(0)).toBe('Click Download CSV Asks first')

    const body = await save()
    expect(body.steps.map((s) => s.intent)).toEqual([
      'Download the CSV',
      'Open the bank',
      'Enter the month {month}',
      'Go to statements',
      'Pick the checking account',
    ])
  })

  it('renames, shares, edits an intent, adds, deletes and makes a step ask first', async () => {
    renderPage()
    await screen.findAllByTestId('step')
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: '  Monthly statement ' } })
    fireEvent.click(screen.getByRole('switch', { name: 'Share with the household' }))
    fireEvent.change(screen.getByLabelText('Step 1 intent'), { target: { value: 'Open the bank site' } })
    fireEvent.click(screen.getByRole('switch', { name: 'Ask first before step 2' }))
    fireEvent.change(screen.getByLabelText('Why step 2 asks'), { target: { value: 'send' } })
    fireEvent.click(screen.getByRole('button', { name: 'Delete step 4' }))
    fireEvent.change(screen.getByLabelText('New step'), { target: { value: 'Rename the file to {month}.csv' } })
    fireEvent.change(screen.getByLabelText('On'), { target: { value: 'desktop' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add step' }))
    expect(steps()).toHaveLength(5)
    // The deleted step's typed value is no longer a candidate.
    expect(screen.queryByRole('switch', { name: 'checking changes each time' })).not.toBeInTheDocument()

    const body = await save()
    expect(body.name).toBe('Monthly statement')
    expect(body.shared).toBe(true)
    expect(body.steps[0].intent).toBe('Open the bank site')
    expect(body.steps[0].action.intent).toBe('Open the bank site')
    expect(body.steps[1].risk).toBe('send')
    expect(body.steps[0]).not.toHaveProperty('risk')
    expect(body.steps.map((s) => s.action.ref ?? s.action.kind)).toEqual(['navigate', 'e3', 'type', 'e9', 'screenshot'])
    expect(body.steps[4]).toEqual({
      intent: 'Rename the file to {month}.csv',
      target: 'desktop',
      action: { kind: 'screenshot', intent: 'Rename the file to {month}.csv' },
    })
  })

  it('edits the success check', async () => {
    renderPage()
    await screen.findAllByTestId('step')
    fireEvent.change(screen.getByLabelText('The page shows (one per line)'), { target: { value: 'Statement ready\n\n' } })
    fireEvent.change(screen.getByLabelText('A file downloads named like'), { target: { value: '' } })
    expect(screen.getByTestId('success-sentence')).toHaveTextContent('the page shows "Statement ready".')
    const body = await save()
    expect(body.success).toEqual({ text_present: ['Statement ready'] })
  })

  it('is read-only to a member who may not change it', async () => {
    api.getWorkflow.mockResolvedValue(bank({ editable: false, owner_id: 9, shared: true }))
    renderPage()
    await screen.findAllByTestId('step')
    expect(screen.getByText(/Only the member who taught it, or a household admin, can change it/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add step' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Step 1 intent')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Name')).toBeDisabled()
    expect(screen.getByRole('switch', { name: 'Share with the household' })).toBeDisabled()
  })
})

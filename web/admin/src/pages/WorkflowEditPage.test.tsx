import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { TaughtWorkflow, WorkflowPut } from '@/api/workflows'
import WorkflowEditPage from './WorkflowEditPage'

const api = vi.hoisted(() => ({ getWorkflow: vi.fn(), saveWorkflow: vi.fn(), relearnWorkflow: vi.fn() }))
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
    expect(screen.getByRole('switch', { name: /^Always asks: .+ before step 5$/ })).toBeChecked()
    expect(screen.getByRole('switch', { name: /^Always asks: .+ before step 5$/ })).toBeDisabled()
    expect(screen.getByRole('switch', { name: 'Ask me first before step 1' })).not.toBeChecked()
    expect(screen.getByTestId('success-sentence')).toHaveTextContent('a file named like statement*.csv downloads.')
  })

  it('saves exactly the fields PUT takes, unchanged when nothing was edited', async () => {
    renderPage()
    await screen.findAllByTestId('step')
    const body = await save()
    expect(Object.keys(body).sort()).toEqual(['name', 'params', 'shared', 'skill', 'steps', 'success', 'triggers'])
    const wf = bank()
    expect(body).toEqual({
      name: wf.name,
      triggers: wf.triggers,
      params: wf.params,
      steps: wf.steps,
      success: wf.success,
      shared: false,
      skill: {},
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
    fireEvent.click(screen.getByRole('switch', { name: 'Ask me first before step 2' }))
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

  // The workflow's skill (docs/agent/WORKFLOW-SKILLS.md): learnt, shown as separate parts, edited here.
  const skilled = () =>
    bank({
      skill: {
        goal: { reason: 'Keep the records', end_state: 'The {month} statement is in Downloads' },
        when_to_use: 'You ask for a bank statement',
        prerequisites: [{ kind: 'signed_in', site: 'bank.example.com', text: 'Signed in to the bank' }],
        guardrails: 'creative',
        notes: [{ text: 'The site signs out after five minutes', app: 'Brave' }],
      },
      steps: bank().steps.map((s, i) =>
        i === 1 ? { ...s, goal: 'Get to the list', expect: { url_contains: 'bank.example.com/statements' } } : i === 2 ? { ...s, expect: { screen: 'The month shows' } } : s,
      ),
    })

  it('shows the skill and saves it unchanged, with what the page does not edit', async () => {
    api.getWorkflow.mockResolvedValue(skilled())
    renderPage()
    await screen.findAllByTestId('step')
    expect(screen.getByLabelText('Why you do this')).toHaveValue('Keep the records')
    expect(screen.getByLabelText("It's done when")).toHaveValue('The {month} statement is in Downloads')
    expect(screen.getByLabelText('Signed in to (prerequisite 1)')).toHaveValue('bank.example.com')
    expect(screen.getByLabelText(/step 2 goal/)).toHaveValue('Get to the list')
    // An expectation the recording proved is read out, not typed over.
    expect(steps()[1]).toHaveTextContent('Then the address contains bank.example.com/statements.')
    expect(screen.getByLabelText(/what you see after step 3/)).toHaveValue('The month shows')
    const body = await save()
    expect(body.skill).toEqual(skilled().skill)
    expect(body.steps[1].expect).toEqual({ url_contains: 'bank.example.com/statements' })
    expect(body.steps[2].expect).toEqual({ screen: 'The month shows' })
  })

  it('edits the goal, prerequisites, notes and a step\'s goal and expectation', async () => {
    api.getWorkflow.mockResolvedValue(skilled())
    renderPage()
    await screen.findAllByTestId('step')
    fireEvent.change(screen.getByLabelText("It's done when"), { target: { value: ' The statement is saved ' } })
    // A prerequisite's value moves with its kind; an empty one and a blank note are not saved.
    fireEvent.change(screen.getByLabelText('Prerequisite 1'), { target: { value: 'page_open' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add a prerequisite' }))
    fireEvent.change(screen.getByLabelText('Prerequisite 2'), { target: { value: 'file_open' } })
    fireEvent.change(screen.getByLabelText('A file is open (prerequisite 2)'), { target: { value: 'budget-{month}.xlsx' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add a prerequisite' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add a note' }))
    fireEvent.change(screen.getByLabelText(/step 1 goal/), { target: { value: 'Start at the bank' } })
    fireEvent.change(screen.getByLabelText(/what you see after step 1/), { target: { value: 'The sign-in page' } })
    fireEvent.change(screen.getByLabelText(/what you see after step 3/), { target: { value: '' } })
    const body = await save()
    expect(body.skill?.goal).toEqual({ reason: 'Keep the records', end_state: 'The statement is saved' })
    expect(body.skill?.prerequisites).toEqual([
      { kind: 'page_open', url: 'bank.example.com' },
      { kind: 'file_open', file: 'budget-{month}.xlsx' },
    ])
    expect(body.skill?.notes).toEqual([{ text: 'The site signs out after five minutes', app: 'Brave' }])
    expect(body.steps[0].goal).toBe('Start at the bank')
    expect(body.steps[0].expect).toEqual({ screen: 'The sign-in page' })
    expect(body.steps[2].expect).toBeUndefined()
    fireEvent.click(screen.getByRole('button', { name: 'Remove prerequisite 1' }))
    expect(screen.getAllByTestId('prerequisite')).toHaveLength(2)
  })

  it('reads the skill out to a member who may not change it', async () => {
    api.getWorkflow.mockResolvedValue({ ...skilled(), editable: false, owner_id: 9, shared: true })
    renderPage()
    await screen.findAllByTestId('step')
    expect(screen.getByText('The {month} statement is in Downloads')).toBeInTheDocument()
    expect(screen.getByText('Signed in to bank.example.com')).toBeInTheDocument()
    expect(screen.getByText('The site signs out after five minutes')).toBeInTheDocument()
    expect(steps()[1]).toHaveTextContent('Get to the list Then the address contains bank.example.com/statements.')
    expect(screen.queryByLabelText('Why you do this')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add a prerequisite' })).not.toBeInTheDocument()
  })

  it('relearns into a draft that changes nothing until it is saved', async () => {
    api.getWorkflow.mockResolvedValue(bank({ recording_id: 'rec_1' }))
    const draft: WorkflowPut = {
      name: 'Get the statement',
      triggers: ['get my bank statement'],
      params: [],
      steps: bank().steps.slice(0, 2),
      success: { download: { name_like: 'statement*.csv' } },
      shared: false,
      skill: { goal: { end_state: 'The statement is in Downloads' } },
    }
    api.relearnWorkflow.mockResolvedValue(draft)
    renderPage()
    await screen.findAllByTestId('step')
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Relearn' }))
    })
    expect(api.relearnWorkflow).toHaveBeenCalledWith('tw_1')
    expect(await screen.findByText('This is a fresh draft from your recording')).toBeInTheDocument()
    expect(steps()).toHaveLength(2)
    expect(screen.getByLabelText('Name')).toHaveValue('Get the statement')
    expect(screen.getByLabelText("It's done when")).toHaveValue('The statement is in Downloads')
    expect(api.saveWorkflow).not.toHaveBeenCalled()

    // Discarding goes back to the stored workflow.
    fireEvent.click(screen.getByRole('button', { name: 'Discard draft' }))
    expect(steps()).toHaveLength(5)
    expect(screen.getByLabelText('Name')).toHaveValue('Download bank statement')

    // Saving the draft stores it and the notice goes.
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Relearn' }))
    })
    const body = await save()
    expect(body.name).toBe('Get the statement')
    expect(body.steps).toHaveLength(2)
    expect(screen.queryByText('This is a fresh draft from your recording')).not.toBeInTheDocument()
  })

  it('says so when the recording is gone', async () => {
    renderPage()
    await screen.findAllByTestId('step')
    expect(screen.getByText('Its recording is no longer kept, so it can’t be learnt again.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Relearn' })).not.toBeInTheDocument()
  })

  it('is Strict until the owner chooses Creative, and saves the choice', async () => {
    renderPage()
    await screen.findAllByTestId('step')
    expect(screen.getByRole('radio', { name: /Strict/ })).toBeChecked()
    fireEvent.click(screen.getByRole('radio', { name: /Creative/ }))
    const body = await save()
    expect(body.skill?.guardrails).toBe('creative')
  })

  it('reads the guardrail out to a member who may not change it', async () => {
    api.getWorkflow.mockResolvedValue(bank({ editable: false, owner_id: 9, shared: true, skill: { guardrails: 'creative' } }))
    renderPage()
    await screen.findAllByTestId('step')
    expect(screen.getByText('Creative.')).toBeInTheDocument()
    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
  })

  it('says a new workflow needs its first run, to its owner and to others', async () => {
    api.getWorkflow.mockResolvedValue(bank({ proven: false }))
    renderPage()
    await screen.findAllByTestId('step')
    expect(screen.getByText('This workflow hasn’t had its first run')).toBeInTheDocument()
    expect(screen.getByText(/Run it once from Workflows while you watch/)).toBeInTheDocument()
  })

  it('says nothing about a first run once the workflow has proved itself', async () => {
    api.getWorkflow.mockResolvedValue(bank({ proven: true }))
    renderPage()
    await screen.findAllByTestId('step')
    expect(screen.queryByText('This workflow hasn’t had its first run')).not.toBeInTheDocument()
  })
})

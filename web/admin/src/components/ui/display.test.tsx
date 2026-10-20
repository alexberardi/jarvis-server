import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import Alert from './Alert'
import Badge from './Badge'
import Banner from './Banner'
import Card, { CardHeader } from './Card'
import EmptyState from './EmptyState'
import ErrorState from './ErrorState'
import List, { ListItem } from './List'
import PageHeader from './PageHeader'
import Skeleton, { SkeletonList } from './Skeleton'
import Table, { TBody, TD, TH, THead, TR } from './Table'

describe('Card', () => {
  it('is a labelled region when it is a section with a heading', () => {
    render(
      <Card as="section" aria-labelledby="h">
        <CardHeader id="h" title="Models" description="What answers" actions={<button>Add</button>} />
      </Card>,
    )
    const region = screen.getByRole('region', { name: 'Models' })
    expect(within(region).getByRole('heading', { level: 2, name: 'Models' })).toBeInTheDocument()
    expect(within(region).getByRole('button', { name: 'Add' })).toBeInTheDocument()
  })

  it('drops its padding when flush', () => {
    const { container } = render(<Card flush>x</Card>)
    expect(container.firstChild).not.toHaveClass('p-4')
  })
})

describe('Badge', () => {
  it('draws its tone on the soft background', () => {
    render(<Badge tone="success">Online</Badge>)
    expect(screen.getByText('Online')).toHaveClass('bg-success-soft', 'text-success', 'text-caption')
  })
})

describe('List', () => {
  it('renders rows as list items, a row with onClick as a button', () => {
    const onClick = vi.fn()
    render(
      <List label="Nodes">
        <ListItem title="Kitchen" description="Online" />
        <ListItem title="Office" onClick={onClick} />
      </List>,
    )
    const list = screen.getByRole('list', { name: 'Nodes' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(2)
    fireEvent.click(screen.getByRole('button', { name: 'Office' }))
    expect(onClick).toHaveBeenCalled()
  })

  it('makes a row with `to` a link', () => {
    render(
      <MemoryRouter>
        <List>
          <ListItem title="Kitchen" to="/admin/nodes/1" />
        </List>
      </MemoryRouter>,
    )
    expect(screen.getByRole('link', { name: 'Kitchen' })).toHaveAttribute('href', '/admin/nodes/1')
  })
})

describe('Table', () => {
  it('has column headers and a screen-reader caption', () => {
    render(
      <Table caption="Recent traces">
        <THead>
          <TR>
            <TH>Name</TH>
          </TR>
        </THead>
        <TBody>
          <TR>
            <TD>chat</TD>
          </TR>
        </TBody>
      </Table>,
    )
    const table = screen.getByRole('table', { name: 'Recent traces' })
    expect(within(table).getByRole('columnheader', { name: 'Name' })).toHaveAttribute('scope', 'col')
    expect(within(table).getByRole('cell', { name: 'chat' })).toBeInTheDocument()
  })
})

describe('PageHeader', () => {
  it('is the page h1 with its actions', () => {
    render(<PageHeader title="Nodes" description="Every room" actions={<button>Pair</button>} />)
    expect(screen.getByRole('heading', { level: 1, name: 'Nodes' })).toBeInTheDocument()
    expect(screen.getByText('Every room')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pair' })).toBeInTheDocument()
  })
})

describe('Alert', () => {
  it('announces a danger alert at once, on the danger tokens', () => {
    render(<Alert tone="danger" title="Couldn't sign in">Check the email and password.</Alert>)
    const a = screen.getByRole('alert')
    expect(a).toHaveTextContent("Couldn't sign in")
    expect(a).toHaveTextContent('Check the email and password.')
    expect(a).toHaveClass('bg-danger-soft')
  })

  it('is a polite status for the other tones', () => {
    render(<Alert tone="warning">Some checks failed.</Alert>)
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByRole('status')).toHaveClass('bg-warning-soft')
  })
})

describe('EmptyState', () => {
  it('can make its title the page heading', () => {
    render(<EmptyState titleAs="h1" title="Page not found" />)
    expect(screen.getByRole('heading', { level: 1, name: 'Page not found' })).toHaveClass('text-heading')
  })

  it('shows the title, description and the action', () => {
    const onAdd = vi.fn()
    render(<EmptyState title="No nodes yet" description="Pair one" action={<button onClick={onAdd}>Pair a node</button>} />)
    expect(screen.getByText('No nodes yet')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Pair a node' }))
    expect(onAdd).toHaveBeenCalled()
  })
})

describe('ErrorState', () => {
  it('is an alert with the message', () => {
    render(<ErrorState message="The server didn't answer" />)
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load this")
    expect(alert).toHaveTextContent("The server didn't answer")
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('calls onRetry and stays busy until it settles', async () => {
    let settle: () => void = () => {}
    const onRetry = vi.fn(() => new Promise<void>((r) => (settle = r)))
    render(<ErrorState onRetry={onRetry} />)
    const button = screen.getByRole('button', { name: 'Try again' })
    fireEvent.click(button)
    expect(onRetry).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(button).toHaveAttribute('aria-busy', 'true'))
    await act(async () => settle())
    expect(button).not.toHaveAttribute('aria-busy')
    expect(button).toBeEnabled()
  })
})

describe('Skeleton', () => {
  it('is hidden from screen readers on its own', () => {
    const { container } = render(<Skeleton />)
    expect(container.firstChild).toHaveAttribute('aria-hidden', 'true')
  })

  it('reads out one loading status for a list', () => {
    render(<SkeletonList rows={4} label="Loading nodes" />)
    const status = screen.getByRole('status', { name: 'Loading nodes' })
    expect(status.children).toHaveLength(4)
  })
})

describe('Banner', () => {
  it('shows its title, text and actions, and passes the role and name through', () => {
    render(
      <Banner tone="danger" role="alert" aria-label="GPU problem" title="The GPU is gone" actions={<button>Fix it</button>}>
        Models that need it are not running.
      </Banner>,
    )
    const alert = screen.getByRole('alert', { name: 'GPU problem' })
    expect(alert).toHaveTextContent('The GPU is gone')
    expect(alert).toHaveTextContent('Models that need it are not running.')
    expect(within(alert).getByRole('button', { name: 'Fix it' })).toBeInTheDocument()
  })

  it('draws its tone on the soft background', () => {
    const { container } = render(<Banner tone="warning">Downloading</Banner>)
    expect(container.firstChild).toHaveClass('bg-warning-soft')
  })
})

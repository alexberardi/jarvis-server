import { useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { AlertOctagon, ArrowUpCircle, Cpu, Inbox, Loader, Plus, Save, Sparkles, Trash2 } from 'lucide-react'
import {
  Alert,
  Badge,
  Banner,
  Button,
  buttonStyles,
  Card,
  CardHeader,
  Dialog,
  EmptyState,
  ErrorState,
  Input,
  List,
  ListItem,
  PageHeader,
  Select,
  Sheet,
  Skeleton,
  SkeletonList,
  Spinner,
  Switch,
  Table,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Tabs,
  toast,
} from '@/components/ui'
import { colors, type ColorToken } from '@/theme/colors'

/**
 * UiGalleryPage shows every base component in every state, on the real shell, for review and
 * screenshots. A dev-only route (/dev/ui): it exists under `vite dev` and in a build made with
 * VITE_UI_GALLERY=1 (scripts/sandbox up --ui), never in a release. `?open=dialog` or
 * `?open=sheet` opens that modal on load, for a screenshot.
 */
export default function UiGalleryPage() {
  const [params] = useSearchParams()
  const [open, setOpen] = useState<string | null>(params.get('open'))
  const close = () => setOpen(null)
  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader
        title="Design system"
        description="Direction B, Quiet Violet: tokens and base components in every state. Not a page users see."
        actions={
          <>
            <Button icon={<Plus size={16} aria-hidden />}>Secondary</Button>
            <Button variant="primary" icon={<Save size={16} aria-hidden />}>
              Primary
            </Button>
          </>
        }
      />

      <div className="space-y-6">
        <Section title="Colour">
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
            {(Object.keys(colors.light) as ColorToken[]).map((t) => (
              <div key={t} className="min-w-0">
                <div className="h-10 rounded-control border border-border" style={{ background: `var(--color-${t})` }} />
                <p className="mt-1 truncate text-caption text-text">{t}</p>
              </div>
            ))}
          </div>
        </Section>

        <Section title="Type">
          <div className="space-y-2">
            <p className="text-title">Title 24 / 650</p>
            <p className="text-heading">Heading 18 / 600</p>
            <p className="text-subheading">Subheading 15 / 600</p>
            <p className="text-body">Body 15 / 400: the default for chat, fields and paragraphs.</p>
            <p className="text-small text-text-muted">Small 13: secondary lines and meta.</p>
            <p className="text-caption text-text-muted">Caption 12 / 500: chips and counters, the floor.</p>
            <p className="font-mono text-small text-text-muted">mono 13: ids, ports, 0.42s</p>
          </div>
        </Section>

        <Section title="Button">
          <Row label="Variants">
            <Button variant="primary">Primary</Button>
            <Button>Secondary</Button>
            <Button variant="ghost">Ghost</Button>
            <Button variant="danger" icon={<Trash2 size={16} aria-hidden />}>
              Delete
            </Button>
          </Row>
          <Row label="Disabled">
            <Button variant="primary" disabled>
              Primary
            </Button>
            <Button disabled>Secondary</Button>
            <Button variant="danger" disabled>
              Delete
            </Button>
          </Row>
          <Row label="Loading">
            <Button variant="primary" loading>
              Saving
            </Button>
            <Button loading>Checking</Button>
          </Row>
          <Row label="Sizes">
            <Button size="sm">Small</Button>
            <Button>Medium</Button>
            <Button size="icon" aria-label="Add">
              <Plus size={18} aria-hidden />
            </Button>
            <Spinner label="Loading" className="text-primary" />
          </Row>
        </Section>

        <Section title="Fields">
          <div className="grid gap-4 md:grid-cols-2">
            <Input label="Name" placeholder="Kitchen" />
            <Input label="With a hint" hint="The household sees this name." defaultValue="Office" />
            <Input label="With an error" error="A node with this name exists." defaultValue="Kitchen" />
            <Input label="Disabled" disabled defaultValue="Can't change" />
            <Select
              label="Model"
              defaultValue="qwen"
              options={[
                { value: 'qwen', label: 'Qwen 3 8B' },
                { value: 'llama', label: 'Llama 3.1 8B' },
              ]}
            />
            <Select label="Placeholder" placeholder="Pick a room" defaultValue="" options={[{ value: 'k', label: 'Kitchen' }]} />
          </div>
          <div className="mt-5 grid gap-4 md:grid-cols-2">
            <SwitchDemo label="Wake word" description="Listen for 'Jarvis' on every node." initial />
            <SwitchDemo label="Check for updates" description="Off: the server never contacts GitHub." />
            <Switch label="Disabled on" checked disabled onCheckedChange={() => {}} />
            <Switch label="Disabled off" checked={false} disabled onCheckedChange={() => {}} />
          </div>
        </Section>

        <Section title="Badge">
          <Row>
            <Badge>Neutral</Badge>
            <Badge tone="primary">Primary</Badge>
            <Badge tone="success" dot>
              Online
            </Badge>
            <Badge tone="warning">Update ready</Badge>
            <Badge tone="danger" dot>
              Offline
            </Badge>
          </Row>
        </Section>

        <Section title="Alert">
          <div className="space-y-2">
            <Alert tone="danger" title="Couldn't sign in">
              Invalid email or password.
            </Alert>
            <Alert tone="warning">Some checks failed. Other devices may not reach this machine until they are fixed.</Alert>
            <Alert tone="success" title="Superuser account created">
              you@example.com
            </Alert>
            <Alert>These are the defaults for the whole install.</Alert>
          </div>
        </Section>


        <Section title="Banner">
          <div className="space-y-3">
            <Banner tone="primary" icon={<Sparkles size={20} />} title="Setup isn't finished" actions={<a href="#banner" className={buttonStyles({ variant: 'primary', size: 'sm' })}>Finish setup</a>}>
              Models, privacy choices and next steps.
            </Banner>
            <Banner tone="warning" icon={<Loader size={20} />}>Language model: downloading 30%.</Banner>
            <Banner tone="success" icon={<ArrowUpCircle size={20} />} title="jarvisd 0.32.0 is available" actions={<Button size="sm">View update</Button>}>
              You have 0.31.2
            </Banner>
            <Banner tone="danger" icon={<AlertOctagon size={20} />} title="The GPU needs a reboot">
              Not running until then: Live (voice replies).
            </Banner>
          </div>
        </Section>

        <div className="grid gap-6 md:grid-cols-2">
          <Card as="section" flush aria-labelledby="gallery-list">
            <div className="p-4 pb-0 md:p-5 md:pb-0">
              <CardHeader id="gallery-list" title="List" description="44px rows, in a flush card." />
            </div>
            <List label="Nodes">
              <ListItem leading={<Cpu size={18} aria-hidden />} title="Kitchen" description="Pi 5 · 192.0.2.10" trailing={<Badge tone="success" dot>Online</Badge>} />
              <ListItem leading={<Cpu size={18} aria-hidden />} title="Office" description="Pressable row" onClick={() => toast('Office')} />
              <ListItem leading={<Cpu size={18} aria-hidden />} title="Garage" description="A link row" to="/dev/ui" />
            </List>
          </Card>
          <Card as="section" flush aria-labelledby="gallery-loading">
            <div className="p-4 pb-0 md:p-5 md:pb-0">
              <CardHeader id="gallery-loading" title="Loading" description="SkeletonList and Skeleton." />
            </div>
            <SkeletonList rows={3} label="Loading nodes" />
            <div className="space-y-2 p-4 md:p-5">
              <Skeleton className="w-3/4" />
              <Skeleton className="w-1/2" />
            </div>
          </Card>
        </div>

        <Section title="Table">
          <Table caption="Recent traces">
            <THead>
              <TR>
                <TH>Request</TH>
                <TH>Node</TH>
                <TH>State</TH>
                <TH className="text-right">Time</TH>
              </TR>
            </THead>
            <TBody>
              <TR>
                <TD>Turn on the kitchen lights</TD>
                <TD>Kitchen</TD>
                <TD>
                  <Badge tone="success">OK</Badge>
                </TD>
                <TD className="text-right font-mono text-small">0.42s</TD>
              </TR>
              <TR>
                <TD>What's the weather tomorrow</TD>
                <TD>Office</TD>
                <TD>
                  <Badge tone="danger">Error</Badge>
                </TD>
                <TD className="text-right font-mono text-small">3.10s</TD>
              </TR>
            </TBody>
          </Table>
        </Section>

        <Section title="Tabs">
          <Tabs
            label="Gallery tabs"
            tabs={[
              { id: 'general', label: 'General', content: <p className="text-text-muted">The General panel.</p> },
              { id: 'voice', label: 'Voice', content: <p className="text-text-muted">The Voice panel.</p> },
              { id: 'beta', label: 'Disabled', content: null, disabled: true },
              { id: 'advanced', label: 'Advanced', content: <p className="text-text-muted">The Advanced panel.</p> },
            ]}
          />
        </Section>

        <div className="grid gap-6 md:grid-cols-2">
          <Card>
            <EmptyState
              icon={<Inbox size={20} />}
              title="Nothing in the inbox"
              description="Workflows and reminders put their results here."
              action={<Button variant="primary">Make a workflow</Button>}
            />
          </Card>
          <Card>
            <ErrorState message="The server didn't answer in 10 seconds." onRetry={() => new Promise((r) => setTimeout(r, 800))} />
          </Card>
        </div>

        <Section title="Dialog, sheet and toast">
          <Row>
            <Button onClick={() => setOpen('dialog')}>Open dialog</Button>
            <Button onClick={() => setOpen('sheet')}>Open sheet</Button>
            <Button onClick={() => toast('Saved')}>Toast</Button>
            <Button onClick={() => toast.success('Node paired')}>Success toast</Button>
            <Button onClick={() => toast.error("Couldn't reach the node")}>Error toast</Button>
          </Row>
        </Section>
      </div>

      <Dialog
        open={open === 'dialog'}
        onClose={close}
        title="Rename node"
        description="The name the household sees and says."
        footer={
          <>
            <Button onClick={close}>Cancel</Button>
            <Button variant="primary" onClick={close}>
              Save
            </Button>
          </>
        }
      >
        <Input label="Name" defaultValue="Kitchen" />
      </Dialog>
      <Sheet
        open={open === 'sheet'}
        onClose={close}
        title="Pick a room"
        footer={<Button onClick={close}>Done</Button>}
      >
        <List label="Rooms">
          <ListItem title="Kitchen" onClick={close} />
          <ListItem title="Office" onClick={close} />
          <ListItem title="Garage" onClick={close} />
        </List>
      </Sheet>
    </div>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = `gallery-${title.toLowerCase().replace(/\W+/g, '-')}`
  return (
    <Card as="section" aria-labelledby={id}>
      <CardHeader id={id} title={title} />
      {children}
    </Card>
  )
}

function Row({ label, children }: { label?: string; children: ReactNode }) {
  return (
    <div className="mb-3 last:mb-0">
      {label && <p className="mb-1.5 text-caption text-text-muted">{label}</p>}
      <div className="flex flex-wrap items-center gap-2">{children}</div>
    </div>
  )
}

function SwitchDemo({ label, description, initial = false }: { label: string; description: string; initial?: boolean }) {
  const [on, setOn] = useState(initial)
  return <Switch label={label} description={description} checked={on} onCheckedChange={setOn} />
}

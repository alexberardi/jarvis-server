import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getServiceSettings } from '@/api/settings'
import { Card, CardHeader, ErrorState, SkeletonList } from '@/components/ui'
import SettingRow from '@/components/settings/SettingRow'
import { errorMessage } from '@/lib/errors'
import { whoIsOn } from '@/lib/settings'

/** One setting on the Phone page: the key, a plain name and, optionally, a line replacing the server's description. */
interface Row {
  key: string
  label: string
  help?: string
}

const SIP: Row[] = [
  { key: 'phone.sip.server', label: 'PBX address', help: 'Host name or IP of the PBX Jarvis registers to as an extension.' },
  { key: 'phone.sip.port', label: 'PBX port', help: "The PBX's SIP port." },
  { key: 'phone.sip.transport', label: 'Transport', help: 'How Jarvis talks to the PBX: udp or tcp, as the extension is set up.' },
  { key: 'phone.sip.extension', label: 'Extension', help: 'The extension Jarvis registers as; also its authentication user.' },
  {
    key: 'phone.sip.password',
    label: 'Extension password',
    help: "The extension's secret on the PBX. After 3 refusals Jarvis stops trying, so the PBX's firewall does not ban this host.",
  },
  {
    key: 'phone.sip.local_port',
    label: 'Local SIP port',
    help: "Jarvis's own SIP port for this account. Keep it fixed so the PBX keeps one contact; each account needs its own.",
  },
  {
    key: 'phone.sip.dial_format',
    label: 'Dial format',
    help: "How numbers go to the PBX's outbound routes: 10-digit (2125550100), 11-digit (12125550100) or e164 (+12125550100). US and Canada only.",
  },
  { key: 'phone.sip.dial_prefix', label: 'Dial prefix', help: 'Digits dialed before every number, if the outbound route needs them (for example 9).' },
]

const TWILIO: Row[] = [
  { key: 'phone.twilio_account_sid', label: 'Account SID' },
  { key: 'phone.twilio_auth_token', label: 'Auth token', help: 'Also checks that media streams come from Twilio.' },
  { key: 'phone.twilio_from_number', label: 'From number', help: 'The Twilio number calls come from, in E.164 (+12125550100), on the same account.' },
]

const LIMITS: Row[] = [
  { key: 'phone_calls.max_call_seconds', label: 'Longest call', help: 'Seconds a call may last before Jarvis hangs up.' },
  { key: 'phone_calls.calls_per_day', label: 'Calls per day', help: 'Calls, and call plans waiting for approval, a household may have each day.' },
  { key: 'phone_calls.monthly_minutes_cap', label: 'Minutes per month', help: 'Call minutes a household may use each month; no more calls once used up.' },
  { key: 'phone_calls.max_concurrent_calls', label: 'Calls at once', help: 'Calls a household may have going at the same time.' },
  { key: 'phone_calls.plan_ttl_minutes', label: 'Approval window', help: 'Minutes a call plan stays valid for approval before it expires.' },
]

const LOOKUP: Row[] = [
  {
    key: 'household.location',
    label: 'Location',
    help: "The household's town, such as Springfield, IL. A business is looked up near it.",
  },
  {
    key: 'web_search.enabled',
    label: 'Web search',
    help: "Lets Jarvis search the web for a business that isn't in the phonebook. This also turns on web search for everything else Jarvis does.",
  },
]

const INBOUND: Row[] = [
  { key: 'phone.inbound.enabled', label: 'Answer calls' },
  { key: 'phone.inbound.answer_delay_seconds', label: 'Ring before answering' },
  { key: 'phone.inbound.max_call_seconds', label: 'Longest call' },
  { key: 'phone.inbound.calls_per_day', label: 'Calls per day' },
  { key: 'phone.inbound.max_concurrent_calls', label: 'Calls at once' },
  { key: 'phone.inbound.transfer_targets', label: 'Transfers' },
]

const PRIVACY: Row[] = [
  { key: 'phone_calls.audio_retention_days', label: 'Keep call audio and transcripts', help: "Days a call's audio and transcript are kept before they are removed." },
]

function Section({ id, title, description, children }: { id: string; title: string; description: ReactNode; children: ReactNode }) {
  return (
    <Card as="section" flush aria-labelledby={id} className="scroll-mt-4" id={`${id}-section`}>
      <CardHeader id={id} title={title} description={description} className="mb-0 px-4 pt-4 pb-3" />
      <div className="divide-y divide-border border-t border-border">{children}</div>
    </Card>
  )
}

/**
 * PhoneSettings is the phone settings grouped by task, edited in place with the Settings page's
 * own rows (cc's `phone_calls` keys and the two that look a business up). The per-user call
 * context (phone_calls.call_context) is PII and edited in the mobile app only.
 */
export default function PhoneSettings() {
  const { data, isPending, isError, error, refetch } = useQuery({
    queryKey: ['settings', 'cc'],
    queryFn: () => getServiceSettings('cc'),
    staleTime: 30_000,
  })
  const cc = data?.services.find((s) => s.service_name === 'cc')

  if (isPending) {
    return (
      <Card flush>
        <SkeletonList rows={6} label="Loading phone settings" />
      </Card>
    )
  }
  if (isError || !cc?.success) {
    return (
      <Card>
        <ErrorState
          title="Couldn't load the phone settings"
          message={isError ? errorMessage(error) : (cc?.error ?? "The command center didn't answer.")}
          onRetry={() => refetch()}
        />
      </Card>
    )
  }

  const byKey = new Map(cc.settings.map((s) => [s.key, s]))
  const rows = (list: Row[]) =>
    list.map((r) => {
      const s = byKey.get(r.key)
      return s && <SettingRow key={r.key} setting={s} serviceName="cc" label={r.label} help={r.help} />
    })
  const enabled = byKey.get('phone_calls.enabled')
  const inbound = byKey.get('phone.inbound.enabled')
  const sip = byKey.get('phone.provider')?.value === 'sip'

  return (
    <>
      <Section
        id="phone-calls"
        title="Calls"
        description="Jarvis places calls only for households where calls are on, and only after someone approves the call plan."
      >
        {enabled && (
          <p role="status" className="px-4 py-2.5 text-small font-medium text-text">
            {whoIsOn(enabled)}
          </p>
        )}
        {rows([
          { key: 'phone_calls.enabled', label: 'Phone calls', help: 'Off by default. Turn it on for the households that may make calls.' },
          {
            key: 'phone.provider',
            label: 'Provider',
            help: 'sip: an extension on your own PBX, such as FreePBX. twilio: a Twilio account, which also needs a public URL for its audio.',
          },
        ])}
      </Section>

      <Section
        id="phone-line"
        title={sip ? 'Phone line: SIP' : 'Phone line: Twilio'}
        description={
          sip
            ? "The install's SIP account, used by every household without its own. Server, extension and password are one set: a household with any of them set uses only its own values, never a mix with these. This page edits the install's account."
            : "The install's Twilio account, used by every household without its own. The SID, token and number are one set: a household with any of them set uses only its own, never a mix with these. Secrets are write-only."
        }
      >
        {rows(sip ? SIP : TWILIO)}
      </Section>

      <Section id="phone-limits" title="Limits" description="Caps for each household's calls. A household can have its own value.">
        {rows(LIMITS)}
      </Section>

      <Section
        id="phone-lookup"
        title="Looking up businesses"
        description="“Call J&G's” finds the number in the household's phonebook first, then, with web search on, searches the web near the household's location."
      >
        {rows(LOOKUP)}
      </Section>

      <Section
        id="phone-inbound"
        title="Answering calls"
        description="Jarvis answers calls the PBX routes to its extension as a receptionist. Phone calls must be on for the household too. On a shared line, keep the default off and turn answering on for one household."
      >
        {inbound && (
          <p role="status" className="px-4 py-2.5 text-small font-medium text-text">
            {whoIsOn(inbound)}
          </p>
        )}
        {rows(INBOUND)}
      </Section>

      <Section
        id="phone-privacy"
        title="Privacy"
        description="Each person's call details (name, callback number, insurance and so on) and each command's “Share with phone calls” switch are edited in the mobile app."
      >
        {rows(PRIVACY)}
      </Section>
    </>
  )
}

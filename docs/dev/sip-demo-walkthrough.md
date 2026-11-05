# SIP demo video walkthrough

Draft for a 12–14 minute recording: configure FreePBX and Jarvis, check the connection,
approve an outbound call, then let Jarvis answer an inbound call and deliver the message.
The audience is someone who already has Jarvis installed and wants to connect a PBX.
FreePBX installation and model downloads happen before recording.

The proposed live demonstration uses the existing local FreePBX VM and a human on a local
softphone. SIP signalling and call audio pass through the VM. The test destination stays
local; a call to a mobile number over the public telephone network would additionally need
a real carrier trunk and its outbound route.

## Recording readiness

Source review on 2026-11-04, updated against server `6ed26869` plus the household-settings
implementation in this change, and mobile `56aee5d`:

| Area | Finding and implication for the video |
| --- | --- |
| FreePBX VM | The disk exists; the PID file's process is no longer running. Start and rehearse before recording. |
| Saved transport evidence | The 2026-10-22 23:24 L2 report passed all four scenarios: registration, outbound audio, inbound audio, and lockout recovery. A later saved drift report matches 150 extension settings. These are historical results, not a rehearsal of this script. |
| Actual conversation | L2 runs the SIP provider with generated speech and a simulated carrier. It does not prove the complete UI → approval → live model conversation → inbox flow. That needs a rehearsal with the full server and a human endpoint. |
| Outbound approval | The phone plan uses `interactive_elements` and editable fields. Mobile renders these and its **Call now** button. The checked web inbox handles an older confirmation shape, so use mobile for approval. |
| Inbound setup | Settings now creates household overrides for all six inbound keys. Use **Set for a household** and **Save**. The install default already enables a single household; for a shared line, keep it off and explicitly enable the intended household. |
| Inbound test | The Phone page observes an incoming offer. It does not enable answering; the receptionist uses the saved household settings. |
| Transfers | The code supports configured destinations. The recorded decision leaves FreePBX transfer behaviour unverified; make this an optional extra after a separate rehearsal. |

This draft does not establish that today's VM, model configuration, mobile build, or human
softphone path is ready. No VM was started and no call was placed while writing it.

## Demo preparation

Use a dedicated Jarvis sandbox, a demo household and a disposable mobile emulator. The
installed assistant and existing household data are not part of this recording.

1. **Bring up the real conversation services.** From the recording checkout:

   ```sh
   scripts/sandbox up --name sip-demo --voice --ui
   ```

   This uses the harness's configured model connection. Verify the `live` and `background`
   model paths, STT and TTS before the rehearsal. Use the sandbox's discovered admin URL and
   credentials. Do not substitute `--llm scripted` or `--llm none` for the live conversation.
   Only describe inference as local if the actual model and speech assignments are local.

2. **Boot the existing PBX without restoring a snapshot:**

   ```sh
   scripts/freepbx-lane start
   ```

   Default VM admin: `http://127.0.0.1:8447`; host SIP: `127.0.0.1:5260` forwarded to the VM's
   5060; forwarded RTP: UDP 30800–30819. Check overrides if these have changed. These are the
   harness defaults, not general FreePBX installation defaults.

3. **Finish and rehearse the human call endpoint.** This is the remaining demo wiring task.
   The saved lane uses a WAV-driven baresip carrier; starting the VM alone does not provide
   a person to answer. Prepare a dedicated microphone/speaker softphone on the host as the
   test carrier endpoint, with the VM's carrier trunk pointing to its actual SIP port.
   Verify outgoing calls to the demo number and incoming calls to test DID `5551230000`.
   Use a headset, and capture both sides on separate audio tracks when possible.

   Use `scripts/freepbx-lane.d/calls.sh` as the reference for the VM network adaptation:
   its external address, forwarded signalling port, RTP range, username-first endpoint
   identification and carrier proxy are required by this QEMU network. Do not present these
   loopback-specific settings as a normal LAN recipe. A physical phone on the LAN cannot
   register directly to the host's `127.0.0.1` listener.

   Reserve extension `100` for the demo Jarvis instance. Do not run the L2 scenario runner
   concurrently: it also registers as `100`. `freepbx-lane calls` and `weekly` restore
   snapshots, and the call suite deliberately tests a host ban; they are separate checks,
   not the command to launch a recording session.

4. **Prepare mobile.** Connect a disposable emulator to this sandbox using the mobile
   harness's documented networking. Sign in as the same demo user who requests the call,
   in the same household. Check that the inbox renders editable **Phone number**, **Details**,
   **Call now**, and **Cancel**. Open the inbox directly; push delivery is not needed.

5. **Prepare household inbound setup.** In the sandbox, sign in to the admin as a superuser.
   Open **Settings**, search `phone.inbound`, and expand **Command center**. Keep the default
   for `phone.inbound.enabled` off. Choose **Set for a household**, select the demo household,
   enable the switch and **Save**. Reload to confirm its own value persists. Use the same
   form to set answer delay, limits or transfer targets for that household if needed.
   For the recording, remove this override with **Use default** so the on-camera setup
   can create it again. Do not change the installed server's settings.

6. **Prepare the destination and props.** Route `2125550100` exclusively to the local human
   endpoint. In mobile **Settings → Phonebook**, save **Demo Cafe** with this number. Use
   fictional caller **Taylor from Demo Delivery** for the inbound example. Keep extension
   secrets masked, and use only demo identities in the inbox and call audio.

7. **Run the whole sequence once.** Registration, Connection, echo, hear extension, request
   a plan, approve from mobile, hear both people, receive the outcome; then place an inbound
   call, leave a message and receive its card. Check screen capture audio before the take.
   Generate fresh call plans during recording: the default approval window is 20 minutes.

## Settings to show

For this isolated single-household demo, keep the complete SIP account at system scope.
Leave household SIP account overrides absent. If using a household-owned account instead,
put the entire account there: partial household credentials do not combine with system ones.

| Setting | Demo value | Explanation for the viewer |
| --- | --- | --- |
| `phone_calls.enabled` | `true` for the demo household, directly or inherited | Enables AI phone calls; off by default. |
| `phone.provider` | `sip` | Uses the PBX. |
| `phone.sip.server` | `127.0.0.1` | This VM is reached through the host. A normal install uses its PBX address. |
| `phone.sip.port` | `5260` | Host's forwarded port; the PBX listens on 5060 inside the VM. |
| `phone.sip.transport` | `udp` | Match the extension's transport. TCP is also supported. |
| `phone.sip.extension` | `100` | Jarvis's dedicated extension. |
| `phone.sip.password` | Extension secret, masked | Copy from the PBX; never show or narrate it. |
| `phone.sip.local_port` | `5062` if free; otherwise a reserved free port | Jarvis's own stable port, unique to this SIP account. |
| `phone.sip.dial_format` | `10-digit` | Matches the test outbound route. `11-digit` and `e164` are alternatives. |
| `phone.sip.dial_prefix` | Empty | Set only if the PBX's route requires extra digits. |
| `phone.inbound.enabled` | `true`, explicitly for the demo household | Assigns answering on this line to that household. |

Briefly show the limits, without editing them just for the video: outbound defaults are
600 seconds per call, 10 calls/plans per day, 60 minutes per month and one concurrent call.
Inbound has separate defaults: 180 seconds, 20 calls per day and one concurrent call;
answer delay is zero for a direct route. The audio-retention setting defaults to 30 days.
Do not promise a playback control on the current inbox screens.

## Recording script

Timing includes clicking and pauses. Spoken narration is in quotes. Jarvis's responses
are live: the descriptions below are expected behaviours, not dialogue to dub over it.

### 0:00–0:40 — What we are setting up

**Picture:** Jarvis admin and FreePBX tabs, with a small diagram:
`Jarvis ↔ local FreePBX VM ↔ demo softphone`.

**Narration:**

“Today we're connecting Jarvis to a phone system using SIP. We'll set up its extension,
enter the connection details, and check the audio. Then we'll ask Jarvis to make a call,
and call it back to leave a message.

“FreePBX is running in a virtual machine on this computer. Our other caller is on a local
softphone, so the demonstration stays here. With a carrier trunk, the PBX can route calls
to and from the public phone network.”

**On-screen caption:** “Prepared demo install · Local SIP calls through FreePBX”.

### 0:40–2:30 — Give Jarvis its own extension

**Picture:** FreePBX **Applications → Extensions**, open the prepared PJSIP extension `100`.
Explain that a new setup starts with **Add Extension → Add New SIP [chan_pjsip] Extension**.
Show the general fields, then Advanced. Keep the Secret covered throughout.

**Narration:**

“Jarvis joins the PBX as an ordinary extension. I've reserved extension 100 for it and named
it Jarvis. We need the extension number and its secret for the next step.

“Set Direct Media to No so the audio stays through the PBX. Keep one contact, and allow a
new registration to replace the old one when Jarvis reconnects. We're using UDP, with ulaw
and alaw available for audio.

“For calls through a carrier, caller ID comes from the extension, outbound route and trunk.
Jarvis doesn't choose an arbitrary number to display.”

**Action:** Show **Submit / Apply Config** if making an actual edit. Briefly show the test
carrier trunk and outbound route, then **Admin → Feature Codes**: `*43` Echo Test and `*65`
Speak Your Exten Number.

**Narration:**

“This route sends our demo number to the local softphone. These two feature codes let Jarvis
check the connection before we have a conversation. On your own PBX, also allow the Jarvis
host through the firewall and intrusion detection.”

**Producer note:** Verify VM network adaptation beforehand. Do not trigger a wrong-password
demo: the saved L2 test shows three refusals can already ban the host, including its access
to the PBX web page. The current password-setting help text overstates protection from bans.

### 2:30–4:30 — Connect Jarvis

**Picture:** Jarvis **Admin → Settings**. Search `phone`; expand **Command center** and its
`phone_calls` category. Use the settings table above as the shot checklist.

**Narration:**

“Now we enable phone calls and select SIP as the provider. The address is our PBX, followed
by its SIP port, transport, extension number and secret.

“The address and port here are specific to our local VM. On a normal network, enter the
PBX's address and the port it actually listens on.

“This local port is Jarvis's own port. Keep it stable and give each SIP account its own.
The dial format must match the PBX's outbound route; ours expects ten digits and no prefix.

“These limits control how many calls Jarvis can place and how long they can run. Requesting
a call creates a plan for us to review. We'll approve that plan before anything is dialled.”

**Action:** Save each edited setting. Keep account values together at one scope. Show
**Admin → Models** briefly if needed to establish the actual speech and model setup.

**Narration:**

“SIP provides the telephone connection. Jarvis also needs working speech recognition,
speech output and a language model to hold the conversation; those are already configured
on this demo server.”

### 4:30–5:45 — Prove the connection and audio

**Picture:** **Admin → Phone**. Hold on extension `100` and **Registered**.

**Narration:**

“Registered means the PBX accepted this extension. Settings can take up to a minute to
reach the SIP connection, so wait for this status before moving on.

“First, Connection checks registration and pings the PBX. Next, Echo test sends speech
through the line and checks what comes back. Hear the PBX calls the feature code that
speaks our extension number.”

**Action:** Run **Connection**, **Echo test (*43)**, then **Hear the PBX (*65)** sequentially.
Show each result, including the actual “Heard” text. Echo can take about 40 seconds; shorten
that wait with a visible “wait shortened” caption. Do not alter the reported timing.

**Narration:**

“Now we've checked registration and audio. Let's use the line for a conversation.”

**Producer note:** Mention **Call my phone** as a quick alternative, not as evidence of a
completed call: its Passed result means it created the approval card. It still needs the
mobile **Call now** tap and an answered call.

### 5:45–6:45 — Ask for the call and approve it

**Picture:** Mobile emulator **Settings → Phonebook**, briefly show **Demo Cafe**, then the signed-in
Jarvis conversation. Keep the softphone visible in a second capture or inset.

**Narration:**

“I've saved Demo Cafe in the household's phonebook with our local test number. We'll give
Jarvis a small task with two things to find out.”

**Presenter's request:**

“Call Demo Cafe and ask what time the kitchen closes tonight, and whether they have oat
milk. Just get the information; don't place an order.”

**Action:** Open the mobile emulator’s **Inbox → Call plan: Demo Cafe**. Show and read **Phone number** and
**Details**, checking that both questions and the no-order instruction are present. Correct
the editable details if necessary, then tap **Call now** once.

**Narration:**

“Here's the destination and the brief. I can correct either before approving. This is the
point where I authorise the call.”

**Producer note:** Use the mobile inbox in this build. A voice request can come from a
signed-in desktop agent; an unidentified speaker on a shared node is refused. Mobile avoids
depending on speaker recognition for this take.

### 6:45–8:15 — The outbound conversation

**Picture:** Softphone rings; the demo participant answers. Show the call timer and retain
the original call audio, including pauses. Presenter narration stops during the exchange.

**Human participant cue sheet:**

- Greeting: “Hello, Demo Cafe, Taylor speaking.”
- Let Jarvis's AI and recording disclosure finish. If asked to continue: “Yes, that's fine.”
- When asked about the kitchen: “The kitchen closes at six thirty tonight. We serve drinks
  until seven.”
- When asked about oat milk: “Yes, we have oat milk. There's a fifty-cent extra charge.”
- If Jarvis checks the distinction: “That's right — food until six thirty, drinks until seven.”
- Closing: “You're welcome. Have a good evening.”

The participant should answer the actual questions in the order asked. If Jarvis asks both
at once, answer both naturally. Do not feed it a predetermined recording or dub a better reply.

**Expected behaviour:** Jarvis discloses that it is an AI and the call is recorded, asks the
two questions, keeps the kitchen and drinks times distinct, and ends without placing an order.
Exact wording and duration will vary.

### 8:15–8:50 — Show what came back

**Picture:** Mobile inbox's **Call finished: Demo Cafe** card; scroll just enough to show the
summary and facts.

**Narration, adapted to the actual result:**

“Here's the result from that call. The kitchen closes at six thirty, drinks continue until
seven, and oat milk is available with a fifty-cent charge. These are the details the other
person gave Jarvis.”

**Producer note:** Read what the real card contains. If a requested fact is missing or wrong,
show that honestly and investigate before selecting a final take. The web inbox can display
the outcome text, but do not promise call-audio playback or a live transcript there.

### 8:50–9:50 — Turn on the receptionist

**Picture:** FreePBX **Connectivity → Inbound Routes**. Show test DID `5551230000` with
destination **Extensions → 100 Jarvis**. Switch to Jarvis **Settings**, search
`phone.inbound.enabled`, and expand **Command center**.

**Narration:**

“For incoming calls, the PBX decides what reaches Jarvis. Here, our test number routes
directly to its extension. You could instead send a call there when another extension
isn't answered.

“Now we choose which household receives those calls. I'm keeping the default off, selecting
Set for a household, choosing our demo home, and turning answering on. Save gives this
household its own value. Use default would remove that override again.

“If your install has just one household, turning on the default also works. When households
share an extension, only one can have answering enabled for that line.”

**Action:** Select the demo household, turn on the switch and **Save**. Show its saved row,
then reload and reopen the setting to prove it persisted. Search `phone.inbound` to show the
answer delay and duration limits. Keep transfer targets empty for this take.

**Narration:**

“We're answering immediately because this is a direct route. The receptionist asks who is
calling, takes their message and sends the household an inbox item. We'll read that in the
mobile emulator, signed in to this same household.”

**Producer note:** The **Inbound call** self-test watches for an incoming offer; it is not
the switch that enables answering. `phone_calls.enabled` must also remain on.

### 9:50–11:20 — Make the inbound call

**Picture:** Human softphone dials the test DID through the local test trunk. Keep the dial
action, connection and conversation together. No presenter narration over the call.

**Expected opening:** Jarvis identifies itself as the household's AI assistant, announces
recording, and asks who is calling and why.

**Human participant cue sheet:**

- First turn: “Hi, this is Taylor from Demo Delivery. Please let them know tomorrow's delivery
  window has moved from ten in the morning to between two and four in the afternoon. They
  don't need to call back.”
- If asked for clarification, answer that question directly.
- If there is a closing exchange: “That's everything. Thank you.”

Give the actual message in the first turn. The current receptionist prompt tells it to
close once it has a name and reason; do not rely on an extra “What is your message?” prompt.

**Expected behaviour:** Jarvis collects the name, reason and message, closes the conversation
and sends a household inbox item. Do not script an exact model response.

### 11:20–12:10 — Read the message and finish

**Picture:** In the mobile emulator, open **Message from Taylor** (or the actual name Jarvis captured) in the household
inbox. Show the reason, message and caller-ID qualification.

**Narration, adapted to the actual card:**

“The household now has the caller's name, the reason for calling and the message: tomorrow's
delivery is between two and four, with no callback needed.

“Caller ID is marked as unverified. The message tells us what the caller said; it doesn't
prove who they are.

“We've connected an extension, checked the line, approved an outgoing call, and received
an incoming message through the same PBX.”

**Final picture:** Hold the outcome and message cards side by side. Caption:
“Outbound calls with approval · Incoming messages · Your SIP phone system”.

## Optional extra — transfer and diagnostics

Add 60–90 seconds only after a separate successful FreePBX rehearsal. Configure a demo
transfer target such as `{"label":"Demo desk","number":"101","when":"the caller is
reporting a delivery that is already at the front door"}`. Extension 101 must have its own
human endpoint. Show a new incoming call meeting that condition and the destination ringing.

Say: “Jarvis can put a caller through to one of the destinations we've configured.” Do not
describe the transfer condition or caller ID as identity verification. A transfer's completion
can mean the target is ringing; once the PBX takes the call, its no-answer handling applies.

For troubleshooting footage, show **SIP trace → Export** with demo data. Explain that it
contains signalling rather than audio and removes authentication material. Phone numbers and
addresses can still be present. Keep this short; the two calls are the main demonstration.

## Source map for revisions

| Claim or recording dependency | Current source |
| --- | --- |
| VM ports, startup and snapshot behaviour | [freepbx-lane](../../scripts/freepbx-lane), [VM network preparation](../../scripts/freepbx-lane.d/calls.sh) |
| L2 coverage and artificial far-end speech | [L2 scenarios](../../tools/siplane/l2.go), [outbound provider scenarios](../../tools/siplane/jarvis.go), [inbound provider scenarios](../../tools/siplane/inbound.go) |
| SIP account resolution and refresh interval | [sipline.go](../../internal/modules/cc/phone/sipline.go) |
| Inbound ownership, settings and message cards | [inbound.go](../../internal/modules/cc/phone/inbound.go) |
| Phone-page diagnostics and labels | [PhonePage.tsx](../../web/admin/src/pages/PhonePage.tsx) |
| Call-my-phone result means plan created | [sipsupport.go](../../internal/modules/cc/phone/sipsupport.go) |
| Phone plan fields, approval and outcomes | [plan.go](../../internal/modules/cc/phone/plan.go), [tool.go](../../internal/modules/cc/phone/tool.go), [cards.go](../../internal/modules/cc/phone/cards.go) |
| Household controls and mobile permissions | [household allowlist](../../internal/modules/cc/household_settings.go), [admin settings](../../internal/modules/admin/bff.go), [HouseholdValues.tsx](../../web/admin/src/components/settings/HouseholdValues.tsx) |
| Household create form | [AddHouseholdValue.tsx](../../web/admin/src/components/settings/AddHouseholdValue.tsx), [scope decision](../decisions/2026-11-04-phone-inbound-household-settings.md) |
| Web inbox confirmation limitation | [inbox.ts](../../web/admin/src/api/inbox.ts) |
| Transfer qualification | [transfer decision](../decisions/2026-10-22-phone-inbound-transfer.md) |
| Actual FreePBX lockout finding | [L2 calls decision](../decisions/2026-10-22-sip-l2-calls-and-lockout.md) |

Mobile approval and phonebook were checked in the separate `jarvis-node-mobile` repository:
`src/screens/Inbox/InboxDetailScreen.tsx`, `src/components/InteractiveElementsSection.tsx`,
`src/screens/Phonebook/PhonebookEditScreen.tsx`, and `src/screens/Settings/SettingsScreen.tsx`.

# Jarvis on FreePBX (phone calls over SIP)

Jarvis can place calls through your own phone system instead of Twilio: it registers to the PBX
as an ordinary SIP extension, like a desk phone, and its calls go out through the PBX's own
outbound routes and trunk. This guide sets that up on **FreePBX 17** (Asterisk 22). Other PBXs
(FreePBX 16, plain Asterisk, FreeSWITCH, 3CX) work the same way: make an extension and give
Jarvis its details. Design and limits: [../cc/phone-sip.md](../cc/phone-sip.md).

> Field names are FreePBX 17's. This guide is text only for now: nobody on the project runs a
> FreePBX, and the screenshots will come from the weekly FreePBX VM. The behaviour it describes
> is tested against an Asterisk 22 configured with FreePBX 17's defaults (the L1 lane,
> `tools/siplane`).

Inbound calls (Jarvis as a receptionist that takes messages) are not built yet: today Jarvis
refuses every call the PBX sends it, and the PBX moves on to the call's next destination. Step 5
prepares for them and is optional.

## 1. Add a PJSIP extension for Jarvis

**Applications → Extensions → Add Extension → Add New SIP [chan_pjsip] Extension.**

- **User Extension:** a free number, for example `1050`.
- **Display Name:** `Jarvis`.
- **Secret:** keep the long one FreePBX generates and copy it; you enter it in Jarvis in step 4.
- **Outbound CID** (on the same page): the number people should see when Jarvis calls them,
  usually your main number, for example `"Home" <2125550100>`. The PBX sets the caller ID, not
  Jarvis: without this the route's or trunk's default is used, and some carriers reject a call
  whose caller ID they do not recognise.

On the extension's **Advanced** tab:

- **Direct Media: No.** With Yes, Asterisk moves the audio to another address in the middle of a
  call; Jarvis copes, but No keeps the audio on one path and is what the tests use.
- **Remove Existing: Yes** (FreePBX 17's default; check it on older installs). FreePBX allows one
  registration per extension (Max Contacts 1). With Remove Existing No, a Jarvis that restarted on a new address is refused until the old
  registration expires, and each refusal counts against it in the firewall (step 3).
- Leave **Max Contacts** at 1, **Media Encryption** off, and the transport on UDP (or TCP, if you
  set Jarvis to TCP too). Codecs: keep **ulaw** and **alaw** allowed.
- **Only if the PBX is not on your local network** (a hosted or cloud PBX, Jarvis at home): set
  **Rewrite Contact**, **RTP Symmetric** and **Force rport** to Yes. On a LAN leave them as they are.

**Submit**, then **Apply Config**.

## 2. Check the feature codes the self-tests use

**Admin → Feature Codes.** Jarvis's self-tests dial two of FreePBX's defaults; both must be
enabled, with their default codes:

- **Echo Test**, `*43`: repeats what it hears, which proves audio both ways.
- **Speak Your Exten Number**, `*65`: says the extension's number.

Jarvis dials no other feature code.

## 3. Let Jarvis through the firewall

FreePBX's Responsive Firewall and Intrusion Detection block a host after a few failed
registrations (a wrong password, or a Jarvis restarting on a new address), from 30 minutes to a
day or more, and sometimes the PBX's web page with it. Jarvis stops after 3 refusals for that
reason, but it is simpler to trust it:

- **Connectivity → Firewall → Networks:** add Jarvis's IP address (for example `192.168.1.20/32`)
  in the **Trusted** zone. Give the Jarvis computer a fixed address (a DHCP reservation in your
  router) first.
- **Intrusion Detection:** add the same address to its **whitelist**.
- **Save** and **Apply**.

If Jarvis was already blocked: **Connectivity → Firewall → Status**, find its address under the
blocked hosts and unblock it.

## 4. Enter the extension in Jarvis

In the Jarvis admin, **Settings → Command center → phone_calls** (for the whole install), or a
household's own value of each setting (for a household with its own PBX):

| Setting | Value |
|---|---|
| `phone.provider` | `sip` |
| `phone.sip.server` | the PBX's address, for example `192.168.1.10` (an IP address avoids DNS trouble) |
| `phone.sip.port` | `5060` (FreePBX's PJSIP port; check **Settings → Asterisk SIP Settings → SIP Settings [chan_pjsip]** if yours differs) |
| `phone.sip.transport` | `udp` (or `tcp`, matching the extension) |
| `phone.sip.extension` | the extension from step 1, for example `1050` |
| `phone.sip.password` | the extension's Secret |
| `phone.sip.local_port` | `5062` (Jarvis's own SIP port; each account needs its own, and it must stay the same) |
| `phone.sip.dial_format` | how your outbound route expects numbers: `10-digit` (`2125550100`), `11-digit` (`12125550100`) or `e164` (`+12125550100`) |
| `phone.sip.dial_prefix` | digits your route needs before every number, for example `9`; usually empty |

`phone_calls.enabled` must also be on for the households that make calls. Jarvis registers
within a minute of the settings changing.

If Jarvis runs in Docker, give the container host networking: the PBX must be able to send calls
and audio back to it. On macOS allow jarvisd through the firewall when asked; on Windows allow
its UDP ports in Windows Defender Firewall.

## 5. Optional: route calls to Jarvis

For when inbound calls are built (Jarvis answers, says it is an automated assistant, asks who is
calling and why, and sends you a message):

- To screen a number: **Connectivity → Inbound Routes**, the route for that number, **Set
  Destination → Extensions →** Jarvis's extension.
- To have Jarvis pick up when you do not: on **your own** extension, **Optional Destinations →
  No Answer → Extensions →** Jarvis's extension.
- Do not put Jarvis in a ring group that rings everyone at once: it would answer first and take
  every call.

Today Jarvis refuses these calls (480), and the PBX moves on to the next destination, voicemail
usually.

### Letting Jarvis put callers through (transfer)

Jarvis can hand a call it answered to one of a few people you choose, with a blind transfer
(SIP REFER). It only ever transfers to the targets you list, and the caller never picks the
number. In Jarvis, set the household's `phone.inbound.transfer_targets` to a list of up to 5:

```json
[{"label": "Alex", "number": "101", "when": "the caller is from Alex's school"},
 {"label": "Sam's mobile", "number": "2125550123", "when": "the caller says it is an emergency about the children"}]
```

- **label** is the name Jarvis uses; **when** is, in your words, when to put a caller through.
  Jarvis judges it from what the caller says, and a caller can say anything: pick conditions
  that do no harm if someone lies. Jarvis never tells callers who it can put through, or when.
- **number** is an extension (`101`) or a number as your outbound routes dial it, digits only.
- **On the PBX:** Asterisk lets an extension transfer by default. If Jarvis's extension refuses
  (the REFER answered 403 or 603 in the SIP trace), Jarvis says it could not put the caller
  through and takes a message instead.
- **Nobody answers:** the PBX reports the transfer done once the target rings, and from then on
  the call is the PBX's: the target's own no-answer destination (its voicemail) applies, not
  Jarvis's. If the target is busy or unreachable, the PBX gives the caller busy itself; Jarvis
  cannot take the call back, and its notification says the transfer failed.
- **External numbers** go out through Jarvis's extension's outbound routes, so they need a route
  that allows them. **Caller ID gotcha:** the transferred call carries the original caller's
  number as its caller ID, and many carriers refuse (or mark as spam) an outgoing call whose
  caller ID is not one of your own numbers. Make the trunk send your own number: on the trunk,
  **Outbound CallerID** your main number and **CID Options: Force Trunk CID**, or on the
  outbound route a **Route CID** with **Override Extension: Yes**. Test it with a call to your
  own mobile before you rely on it.

## 6. Check it from the admin

**Admin → Phone** shows each SIP account:

- **Registered**, with the round-trip times of the last REGISTER and of the OPTIONS keepalive.
  Anything else comes with what went wrong and what to do.
- **Self-tests**, best run in this order:
  1. **Connection**: registers again and pings the PBX.
  2. **Echo test (\*43)**: plays a sentence into the echo test and checks what comes back: audio
     both ways, the codec, the delay.
  3. **Hear the PBX (\*65)**: checks that Jarvis hears the PBX say its extension number.
  4. **Call my phone**: sends you the usual confirm card for a call to your own phone. Answer
     it and check the caller ID your phone shows, and that the call went out at all (the dial
     format and prefix).
  5. **Inbound call**: call Jarvis's extension within 60 seconds; the page shows the caller ID
     and the audio the PBX offered.
- **SIP trace**: the last 20 calls' SIP messages and recent registrations, with passwords and
  digest responses removed and no audio. **Export** saves it as a text file to send with a
  support request.

## Troubleshooting

| What the Phone page says | What to do |
|---|---|
| The PBX did not accept the extension's password | Copy the Secret from the extension again into `phone.sip.password`. |
| Stopped; the PBX may have blocked this host | Unblock Jarvis under **Firewall → Status**, add it as Trusted (step 3), correct the password, then save the settings (or restart Jarvis) to try again. |
| The PBX refused this host (403) | The firewall blocked it, or another device holds the extension: Remove Existing = Yes (step 1), and no desk phone registered with the same extension. |
| The PBX does not know this extension | Check `phone.sip.extension`, and that the extension was saved and **Apply Config** pressed. |
| The PBX did not answer | Check the address and port, that Jarvis can reach the PBX (same network or VPN), and the firewall. |
| Echo test: heard nothing back | Audio is blocked one way: Direct Media = No; firewalls and NAT between them; on a remote PBX the NAT settings in step 1. |
| Echo test: came back unclear | Allow ulaw and alaw on the extension; check the network for packet loss. |
| \*43 or \*65 not answered | Enable the feature code (step 2). |
| Call my phone: wrong caller ID | Set the extension's Outbound CID (step 1); the trunk may also override it. |
| Call my phone: "cannot be completed as dialed" | Change `phone.sip.dial_format` or `phone.sip.dial_prefix` to what your outbound route's dial patterns expect. |
| Inbound call: nothing arrived | Check the inbound route or the No Answer destination (step 5), and that the extension shows as available in the PBX. |

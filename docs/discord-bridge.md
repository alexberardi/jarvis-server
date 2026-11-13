# Discord bridge

The Discord bridge lets people talk to Jarvis from anywhere, with no VPN, by messaging a Discord
bot. It works by text, by voice clip, with photos, with slash commands, and live in a voice channel,
like talking to a node at home. Jarvis answers only what it is sent, in the place it was sent.

This guide covers what it does, why it works the way it does, how to set it up (once, by a household
admin) and how people use it.

## What it does

- **Text chat.** Message the bot in a direct message, or post in the Jarvis channel. In the channel
  Jarvis answers in a thread started from your message, so people do not talk over each other.
- **Voice clips.** Send a Discord voice message. Jarvis transcribes it, shows what it heard, and
  answers with a voice message of its own when you spoke to it.
- **Photos.** Attach a photo and ask about it, exactly as in the Jarvis app.
- **Slash commands.** `/jarvis ask`, `new`, `link`, `node`, `voice`, `notify`, `join`, `leave` and `status`.
- **Live voice.** Join the Jarvis voice channel, run `/jarvis join`, and talk. Jarvis answers aloud and
  stops if you talk over it.
- **Reminders and alarms.** If you opt in, reminders you set are also sent to your Discord direct
  messages. This is off by default.
- **Reactions.** Jarvis adds 👀 when it has seen your message and ✅ when it has answered.

Jarvis never starts a conversation. The only thing it sends on its own is a reminder, to the person
who set it, in their direct messages, and only if they turned that on.

## Why it works this way

- **The bot connects out to Discord.** Nothing is opened on your network, so there is no port to
  forward and no VPN to keep running. That is what makes it work from anywhere.
- **It is the mobile chat, not a second brain.** A Discord message runs through the same pipeline as
  a message typed in the Jarvis app: the same node, tools, memory and speaker. Discord is only a
  different way in. A linked Discord account acts as that person.
- **The bot's secrets go in through the app.** The bot token is entered in the Jarvis app by a
  household admin (household settings, like the Twilio credentials) and is never put in a file,
  compose or the environment. See [Security](#security).
- **People link themselves.** Each person proves their Discord account belongs to them with a
  one-time code from the app, so nobody types account IDs into a config file.
- **One bot per household.** Each household uses its own Discord application and token.

```text
Discord  --->  bot (outbound connection)  --->  link check  --->  chat turn (node, tools, memory)
   ^                                                                       |
   +----------- reply, in the same DM, thread or voice channel  <----------+
```

## Security

- **Only linked accounts get an answer.** Everyone else is ignored silently: other people in the
  server, other bots, other channels. A stranger's voice in the voice channel is dropped before it is
  decoded.
- **A linked Discord account acts as that Jarvis user.** Treat it like a login: turn on two-factor
  authentication for the Discord account, keep the server private and invite nobody you do not trust.
- **Linking uses a one-time code** that lasts ten minutes, works once, and is replaced if you ask for
  another. Five wrong guesses lock that Discord account out for ten minutes.
- **The bot token is write only.** The server shows it as `********` once saved, and it is not logged.
  It is stored in the server's settings database in the same way as every other secret in Jarvis
  (for example the Twilio token). That store is not encrypted at rest, so protect the server's
  data folder the way you protect the rest of Jarvis. If a token leaks, choose **Reset Token** on the
  Discord application's **Bot** tab and enter the new one in the app.
- **Replies never mention anyone**, whatever the text says.
- **Downloads are limited.** Photos and voice clips are fetched only from Discord's own file hosts
  (`cdn.discordapp.com` and `media.discordapp.net`) over HTTPS, with a size cap, and clips are
  limited to two minutes.
- **Live voice records nothing.** Audio is held in memory only until it has been transcribed, then
  dropped. The bot announces in the Jarvis channel when it joins and leaves a voice channel, and
  leaves when nobody else is there.
- **Reminders are opt in** and go only to your own direct messages, never to the channel.

## Setup

Do these once, in order. Steps 1 to 6 are in Discord. Never paste the bot token into a chat or
a session.

### 1. Create a private server

1. In Discord choose **Add a Server**, **Create My Own**, **For me and my friends**.
2. Invite nobody and create no invite links.

### 2. Create the channels

1. In the server choose **Create Channel**, then **Text**, and name it `#jarvis`.
2. Create a second channel, choose **Voice**, and name it `Jarvis`. This is the only voice channel the
   bot will join.
3. If you make either channel private, open **Edit Channel**, **Permissions** and add the bot's role
   after the bot has joined (step 6). It needs View Channel and Send Messages on `#jarvis`, and View
   Channel, Connect and Speak on the `Jarvis` voice channel.

You do not create threads. The bot creates them itself.

### 3. Copy the IDs

1. Open **User Settings**, **Advanced** and turn on **Developer Mode**.
2. Right-click the server, the `#jarvis` channel and the `Jarvis` voice channel, and choose **Copy ID**
   for each.
3. Keep the server ID and both channel IDs. They are not secrets. (Your own user ID is not needed:
   you link by code in step 8.)

### 4. Create the application

1. Go to <https://discord.com/developers/applications> and choose **New Application**.
2. Name it `Jarvis`.

### 5. Configure the bot

On the **Bot** tab:

1. Choose **Reset Token** and copy it. It is shown once. Store it in your password manager straight away.
2. Turn **Public Bot** off, so nobody else can add it. Discord can refuse this while the application
   has a default install link. If so, open the **Installation** tab and clear the install link first.
3. Leave **Requires OAuth2 Code Grant** off.
4. Under **Privileged Gateway Intents** turn **Message Content Intent** on. Leave **Server Members
   Intent** and **Presence Intent** off.

### 6. Invite the bot with minimal permissions

1. With the application open, choose **OAuth2** in the left sidebar and scroll to **OAuth2 URL Generator**.
2. Under **Scopes** tick `bot` and `applications.commands`. The **Bot Permissions** panel appears once
   `bot` is ticked.
3. Under **Bot Permissions** tick exactly these eleven. Leave Send TTS Messages and Video off: Send TTS
   Messages is Discord's own read-aloud feature (not Jarvis's voice), and video is not supported:

   | Permission               | Why                                          |
   | ------------------------ | -------------------------------------------- |
   | View Channels            | See `#jarvis`                                |
   | Send Messages            | Reply in the channel and in direct messages  |
   | Create Public Threads    | Start a thread for each channel conversation |
   | Send Messages in Threads | Reply inside those threads                   |
   | Read Message History     | Read the message being answered              |
   | Attach Files             | Send images and files back                   |
   | Send Voice Messages      | Reply with a voice clip                      |
   | Add Reactions            | React 👀 when read and ✅ when done          |
   | Embed Links              | Show link previews in replies                |
   | Connect                  | Join the voice channel for live conversation |
   | Speak                    | Talk in the voice channel                    |

4. Leave **Integration Type** as **Guild Install**, copy the **Generated URL**, open it in a browser,
   pick your private server, then choose **Continue** and **Authorise**.
5. The bot appears in the member list. It stays offline until step 7 is done.

If you invited the bot before adding a permission to this list, generate the URL again and
re-authorise. Channel permission overrides carry over.

### 7. Enter the bot in the Jarvis app (household admin)

1. Open the Jarvis app, then **Settings**, your household, and find the **Discord** card.
2. Choose **Set up**. Either copy the bot details (token, server ID, channel IDs) from your password
   manager and choose **Paste details from clipboard**, or type them in. The paste understands lines
   like `Bot Token: ...`, `Server ID: ...`, `Channel ID: ...`, `Voice Channel ID: ...`, and JSON with
   `token`, `guild_id`, `channel_id` and `voice_channel_id`.
3. The voice channel ID is optional. Leave it empty and there is no live voice.
4. Choose **Save**. The bridge turns on, and within about half a minute the card says "On and connected".

The token is write only: once saved, the app shows only that it is set. **Replace** enters a new one,
and **Remove** deletes it and turns the bridge off.

### 8. Link each person (every member)

1. In the app, in the same **Discord** card, choose **Link my Discord**. If the household has several
   nodes, pick the one Jarvis should answer through first.
2. The app shows an eight character code.
3. In Discord, send the bot `/jarvis link` with that code (the command has a `code` box). It answers
   privately: "Linked."
4. The app now shows "Linked". There you can turn **Send me reminders and alarms** on, choose **Voice
   replies**, or unlink.

## Using it

### Messages

- **Direct message** the bot. You must share a server with it.
- Or post in `#jarvis`. Jarvis starts a thread from your message and answers in it. Keep talking in
  the thread to continue the same conversation.
- Conversations expire after ten minutes without a message, like the app's.

### Where commands work

Messages and commands work in direct messages, in `#jarvis` (and its threads), and in the text chat of
the `Jarvis` voice channel, so you can run `/jarvis join` right where you are. Anywhere else the bot
tells you privately that it only answers in those places.

### Slash commands

| Command                           | What it does                                                     |
| --------------------------------- | ---------------------------------------------------------------- |
| `/jarvis ask message:<text>`      | Ask Jarvis something. The answer is visible to the channel       |
| `/jarvis new`                     | Start a fresh conversation                                       |
| `/jarvis link code:<code>`        | Link your Discord account (see step 8)                           |
| `/jarvis node name:<node>`        | Choose which Jarvis node answers                                 |
| `/jarvis voice mode:<mode>`       | `auto`, `always` or `never` voice replies                        |
| `/jarvis notify state:<on or off>`| Reminders and alarms in your direct messages                     |
| `/jarvis join`                    | Join the Jarvis voice channel (you must be in it)                |
| `/jarvis leave`                   | Send the bot out of the voice channel                            |
| `/jarvis status`                  | Show your link and settings                                      |

Replies to commands other than `ask` are private to you.

### Voice clips

Hold the microphone button in Discord and send a voice message to the bot (or in `#jarvis`). Jarvis
replies with "Heard: ..." so a mishearing is obvious, then the answer. With voice replies on `auto`,
a voice clip gets a voice clip back and typing gets text.

### Photos

Attach up to four photos (JPEG, PNG or WebP) with a question, for example "translate this" or "what does
this label say". Jarvis shrinks a big phone photo to the size the chat accepts, so you can send it as
it comes from the camera. This needs a Jarvis model that can see images, as in the app. A photo with
no words is sent on as it is.

### Live voice

1. Join the `Jarvis` voice channel yourself.
2. Run `/jarvis join` (in the voice channel's own text chat, or in `#jarvis`). The bot joins, and posts a
   notice in `#jarvis`.
3. Talk. When you stop for about a second, Jarvis answers aloud. Start talking while it is
   speaking and it stops.
4. `/jarvis leave` sends it away. It also leaves on its own a minute after everyone else has left.

Only linked people are heard. The bot needs `libopus` installed on the server (see
[Server requirements](#server-requirements)).

### Reminders and alarms

With **Send me reminders and alarms** on (the app, or `/jarvis notify state:on`), a reminder you set
is also sent to your Discord direct messages, as text, when it fires. Nothing else is ever sent
unasked, and it never goes to the channel. Turn it off the same way.

## Server requirements

- Speech to text and text to speech configured, for voice clips and live voice.
- `libopus` for voice clips and live voice (Debian and Ubuntu: `libopus0`; the container image
  includes it). Jarvis loads it at run time, so jarvisd still builds without cgo. Without it, text,
  photos and slash commands work and voice features tell you what is missing.
- Outbound HTTPS and WebSocket access to Discord (`discord.com`, `gateway.discord.gg`, the voice
  servers on UDP) and to `cdn.discordapp.com`.

## Troubleshooting

| Symptom                                    | Check                                                                                                             |
| ------------------------------------------ | ----------------------------------------------------------------------------------------------------------------- |
| The Discord card says "not connected"      | The token, server ID and channel ID are right; Message Content Intent is on; wait 30 seconds                      |
| Bot is online but never answers            | You have linked your account; you are in `#jarvis` or a DM; the node is online                                    |
| No thread appears in the channel           | The bot has Create Public Threads and Send Messages in Threads on `#jarvis`                                       |
| "Node ... not found" in the reply          | Choose a node with `/jarvis node`, or relink with a node picked in the app                                        |
| Cannot direct message the bot              | You must share a server with it                                                                                   |
| The bot says "went in as a normal message" | You typed `/jarvis ...` instead of picking it: type `/`, choose **jarvis**, then the command, and fill in its box |
| Slash commands are missing                 | The invite URL included the `applications.commands` scope; re-invite the bot                                      |
| "I couldn't make out that clip"            | The clip was silent, over two minutes, or speech to text is down                                                  |
| Voice reply missing                        | `/jarvis voice` is not `never`; the bot has Send Voice Messages; text to speech is working                        |
| `/jarvis join` says join the channel       | You must be in the configured `Jarvis` voice channel first                                                        |
| "Live voice needs libopus"                 | Install `libopus0` on the server                                                                                  |
| Joins the voice channel but hears nothing  | The bot has Connect and Speak on that channel; you are linked                                                     |
| Public Bot will not turn off               | Clear the default install link on the **Installation** tab                                                        |
| Token rejected                             | Reset the token on the **Bot** tab and enter the new one in the app                                               |

## Limits

- One text channel and one voice channel per household.
- Video is not supported.
- Live voice uses Discord's end-to-end encrypted voice (the DAVE protocol) through the pure Go
  `dave-go` library, which is young (version 0.5) and has had no independent security audit. If
  Discord changes the protocol, live voice may stop until the library is updated. Text, clips,
  photos and commands do not depend on it.
- Discord voice messages are limited to two minutes here.

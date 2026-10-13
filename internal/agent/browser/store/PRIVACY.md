# Jarvis Browser Control: privacy policy

*Last updated: 12 October 2026. Applies to the Jarvis Browser Control extension for Firefox,
Chrome and other Chromium-based browsers.*

Jarvis is a voice assistant that you run yourself. This extension lets your Jarvis work with your
web browser. In short: **it reads what you ask it to read, sends it only to your own Jarvis, and
keeps nothing.**

## What the extension reads, and when

It reads something only when you ask Jarvis to do something in your browser, for example "close
the YouTube tab" or "read me this page".

- **Your open tabs:** their titles and web addresses, when you ask about your tabs or ask Jarvis
  to switch to, close or reload one.
- **The text of a page:** the words you can see on the page, when you ask Jarvis to read it or
  to find something on it.
- **Video and audio on a page:** whether it's playing, and its position, when you ask Jarvis to
  play, pause, skip or mute it.

It **never** reads what you type into a website: no passwords, no text boxes, no form fields. It
doesn't type, click or fill anything in for you.

Two small things are sent without a request. When the extension connects, it tells the Jarvis
desktop app which browser it is running in and its version (for example "Firefox 140"). And when
you switch to or away from a browser window, the
extension tells Jarvis "this browser is in front" or "it isn't". It doesn't say which page or
tab. Jarvis uses both only to know which browser you mean if you use more than one.

## Where it goes

Only to **your own Jarvis**:

1. The extension passes it to the Jarvis desktop app on the same computer, through a private
   channel built into the browser.
2. The desktop app passes it to the Jarvis server that you (or your household) run.

That server uses it to answer your request, for example to read the page aloud. The extension
itself has no internet connection of its own.

Jarvis's makers never receive it. It is not sold, shared, or given to anyone else, and it isn't
used for advertising or anything other than doing what you asked.

What your Jarvis server keeps afterwards, such as a conversation history, is up to your own
server and its settings. It stays on your server.

## What the extension doesn't do

- No analytics, tracking or advertising.
- No third parties of any kind.
- Nothing is stored by the extension. It doesn't save anything in your browser; it forgets each
  request once it has answered it.
- Websites and other extensions can't send it commands. It listens only to the Jarvis desktop app
  on your computer.

## Open source

The extension and the rest of Jarvis are open source, so anyone can check what they do:
<https://github.com/alexberardi/jarvis-server>.

## Questions

Ask on GitHub: <https://github.com/alexberardi/jarvis-server/issues>. If this policy changes, the
new version will be published at this same address.

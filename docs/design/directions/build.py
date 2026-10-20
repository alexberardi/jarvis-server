"""Builds directions.html (the three directions side by side, with live Dashboard and Chat mockups)
and contrast.md from tokens.py. Self-contained: inline CSS, a few lines of JS, no fetches.

    python3 docs/design/directions/build.py

directions.html?frame=<A|B|C>-<dash|chat>-<desk|phone>&theme=<light|dark> shows one mockup at 1:1,
which is how the PNGs next to it were rendered (render.mjs).
"""
import html
import os

from tokens import DIRECTIONS, checks

HERE = os.path.dirname(os.path.abspath(__file__))
esc = html.escape

# Inline lucide-style icons (24px grid, stroke), so the mockups need nothing external.
ICONS = {
    "chat": '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>',
    "inbox": '<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/>',
    "flow": '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/><path d="M10 6.5h4a2 2 0 0 1 2 2V14"/>',
    "video": '<rect x="2" y="6" width="14" height="12" rx="2"/><path d="m22 8-6 4 6 4z"/>',
    "puzzle": '<path d="M12 2v4a2 2 0 1 0 4 0V2h4v6h-2a2 2 0 1 0 0 4h2v8H4V2z"/>',
    "home": '<path d="m3 10 9-7 9 7v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M9 22V12h6v10"/>',
    "box": '<path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="m3 8 9 5 9-5M12 13v8"/>',
    "cpu": '<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>',
    "gear": '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
    "link": '<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
    "list": '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
    "pulse": '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
    "users": '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.9M16 3.1a4 4 0 0 1 0 7.8"/>',
    "up": '<circle cx="12" cy="12" r="10"/><path d="m16 12-4-4-4 4M12 16V8"/>',
    "menu": '<path d="M4 6h16M4 12h16M4 18h16"/>',
    "plus": '<path d="M12 5v14M5 12h14"/>',
    "send": '<path d="m22 2-7 20-4-9-9-4z"/><path d="M22 2 11 13"/>',
    "mic": '<rect x="9" y="2" width="6" height="12" rx="3"/><path d="M19 10v2a7 7 0 0 1-14 0v-2M12 19v3"/>',
    "clip": '<path d="m21.4 11-8.6 8.6a5.5 5.5 0 0 1-7.8-7.8l8.6-8.6a3.7 3.7 0 0 1 5.2 5.2l-8.6 8.6a1.8 1.8 0 0 1-2.6-2.6l8-8"/>',
    "copy": '<rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>',
    "alert": '<path d="m21.7 18-8-14a2 2 0 0 0-3.4 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.7-3z"/><path d="M12 9v4M12 17h.01"/>',
    "check": '<path d="M20 6 9 17l-5-5"/>',
    "lock": '<rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>',
    "brain": '<path d="M12 5a3 3 0 1 0-5.9.8A4 4 0 0 0 4 13a4 4 0 0 0 4 6h4zM12 5a3 3 0 1 1 5.9.8A4 4 0 0 1 20 13a4 4 0 0 1-4 6h-4z"/>',
    "refresh": '<path d="M21 12a9 9 0 0 1-15.5 6.3L3 16M3 12a9 9 0 0 1 15.5-6.3L21 8"/><path d="M21 3v5h-5M3 21v-5h5"/>',
    "more": '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
    "chev": '<path d="m6 9 6 6 6-6"/>',
}


def icon(name, cls="ic"):
    return f'<svg class="{cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">{ICONS[name]}</svg>'


# Per-direction copy where the directions differ in voice.
COPY = {
    "A": {"dash_title": "Home", "dash_sub": "Avery Household", "greet": "Good afternoon, Jordan.",
          "status": "Jarvis is running. One thing needs you.", "chat_ph": "Ask Jarvis anything", "nav_admin": "Your server"},
    "B": {"dash_title": "Dashboard", "dash_sub": "Avery Household · jarvisd 0.31.2 · up 4 days", "greet": "",
          "status": "", "chat_ph": "Message Jarvis", "nav_admin": "Admin"},
    "C": {"dash_title": "Dashboard", "dash_sub": "jarvisd 0.31.2 · jarvis-host · up 4d 2h", "greet": "",
          "status": "", "chat_ph": "Message Jarvis", "nav_admin": "Server"},
}

NAV_USER = [("chat", "Chat", ""), ("inbox", "Inbox", "2"), ("flow", "Workflows", ""), ("video", "Recordings", ""), ("puzzle", "Add-ons", "")]
NAV_ADMIN = [("home", "Dashboard", ""), ("box", "Models", ""), ("cpu", "Nodes", ""), ("gear", "Settings", ""), ("link", "Connections", ""),
             ("list", "Logs", ""), ("pulse", "Traces", ""), ("users", "Users", ""), ("up", "Updates", "")]


def sidebar(k, active):
    def item(ic, label, badge):
        cur = ' aria-current="page"' if label == active else ""
        b = f'<span class="m-badge">{badge}</span>' if badge else ""
        return f'<a class="m-nav"{cur}>{icon(ic)}<span>{label}</span>{b}</a>'
    first = "Dashboard" if k != "A" else "Home"
    admin = [(i, first if l == "Dashboard" else l, b) for i, l, b in NAV_ADMIN]
    return (f'<aside class="m-side"><div class="m-brand"><span class="m-mark"></span>Jarvis Automation</div>'
            f'<nav>{"".join(item(*n) for n in NAV_USER)}<div class="m-navgroup">{COPY[k]["nav_admin"]}</div>'
            f'{"".join(item(*n) for n in admin)}</nav>'
            f'<div class="m-me"><span class="m-avatar">J</span><span><b>Jordan</b><small>Avery Household</small></span></div></aside>')


def topbar_phone(title):
    return (f'<header class="m-ptop"><button class="m-icbtn" aria-label="Menu">{icon("menu")}</button>'
            f'<span class="m-ptitle">{title}</span><span class="m-avatar sm">J</span></header>')


def dashboard(k, vp):
    c = COPY[k]
    greet = (f'<section class="m-greet"><p class="t-display">{c["greet"]}</p><p class="m-greet-sub">{c["status"]}</p></section>'
             if c["greet"] else "")
    setup = ('<div class="m-banner"><div><b>Setup isn\'t finished.</b><span> Models, privacy choices and next steps: 2 of 11 left.</span></div>'
             '<button class="m-btn primary">Finish setup</button></div>')
    attention = (f'<section class="m-card m-attn"><h2 class="t-heading">{icon("alert", "ic warn")}Needs you</h2>'
                 '<div class="m-issue"><p class="m-issue-t">Phones and nodes on your network can\'t reach Jarvis</p>'
                 '<p class="m-muted">The firewall drops 14 of jarvisd\'s ports from 192.168.1.0/24. It works from this machine only.</p>'
                 '<div class="m-code"><code>sudo ufw allow from 192.168.1.0/24 to any port 7700:7712 proto tcp</code>'
                 f'<button class="m-icbtn" aria-label="Copy command">{icon("copy")}</button></div>'
                 '<div class="m-row-actions"><button class="m-btn">Check again</button><a class="m-link">Why this matters</a></div></div>'
                 '<p class="m-muted m-ok">4 other checks pass.</p></section>')
    rooms = (f'<section class="m-card"><div class="m-cardhead"><h2 class="t-heading">{icon("cpu")}Rooms</h2><a class="m-link">All nodes</a></div>'
             '<ul class="m-list">'
             '<li><span class="m-dot ok"></span><span class="m-li-main">Kitchen<small>Pi 5 · last heard 12 s ago</small></span><span class="m-chip ok">Online</span></li>'
             '<li><span class="m-dot ok"></span><span class="m-li-main">Living room<small>Pi 4 · last heard 40 s ago</small></span><span class="m-chip ok">Online</span></li>'
             '<li><span class="m-dot"></span><span class="m-li-main">Avery\'s laptop<small>Desktop agent · 3 h ago</small></span><span class="m-chip">Asleep</span></li>'
             '</ul></section>')
    models = (f'<section class="m-card"><div class="m-cardhead"><h2 class="t-heading">{icon("box")}Models</h2><a class="m-link">Manage</a></div>'
              '<ul class="m-list">'
              '<li><span class="m-li-main">Voice replies<small>qwen3-14b · remote</small></span><span class="m-chip ok">Ready</span></li>'
              '<li><span class="m-li-main">Memory and planning<small>qwen3-14b · remote</small></span><span class="m-chip ok">Ready</span></li>'
              '<li><span class="m-li-main">Speech to text<small>Needed for voice</small></span><button class="m-btn sm">Set up</button></li>'
              '<li><span class="m-li-main">Text to speech<small>Needed for voice</small></span><button class="m-btn sm">Set up</button></li>'
              '</ul></section>')
    recent = (f'<section class="m-card m-recent"><div class="m-cardhead"><h2 class="t-heading">{icon("pulse")}Recent requests</h2><a class="m-link">All traces</a></div>'
              '<ul class="m-list">'
              '<li><span class="m-li-main">"Turn off the kitchen lights"<small>Kitchen · Jordan</small></span><span class="m-num">412 ms</span><span class="m-num m-muted">2 min</span></li>'
              '<li><span class="m-li-main">"Remember the dog walker comes at five"<small>Phone · Jordan</small></span><span class="m-num">1.2 s</span><span class="m-num m-muted">9 min</span></li>'
              '<li><span class="m-li-main">"What\'s on tomorrow?"<small>Living room · Robin</small></span><span class="m-num">2.8 s</span><span class="m-num m-muted">1 h</span></li>'
              '<li><span class="m-li-main">"Set a timer for pasta"<small>Kitchen · Sam</small></span><span class="m-chip bad">Failed</span><span class="m-num m-muted">3 h</span></li>'
              '</ul></section>')
    system = (f'<section class="m-card m-system"><div class="m-cardhead"><h2 class="t-heading">{icon("gear")}This server</h2>'
              f'<button class="m-icbtn" aria-label="Server actions">{icon("more")}</button></div>'
              '<dl class="m-kv"><dt>Host</dt><dd>jarvis-host · linux/amd64</dd><dt>Hardware</dt><dd>24 cores · 31 GB · RTX 4090</dd>'
              '<dt>Data</dt><dd>5.5 MB · 197 GB free</dd><dt>Listeners</dt><dd>11 serving</dd></dl></section>')
    title = c["dash_title"]
    head = f'<div class="m-pagehead"><div><h1 class="t-title">{title}</h1><p class="m-muted">{c["dash_sub"]}</p></div></div>'
    if k == "A":
        head = ""
    body = (f'<div class="m-page dash">{head}{greet}{setup}<div class="m-grid"><div class="m-col">{attention}{models}</div>'
            f'<div class="m-col">{rooms}{recent}{system}</div></div></div>')
    if vp == "desk":
        return f'<div class="m-app">{sidebar(k, title)}<main class="m-main">{body}</main></div>'
    return f'<div class="m-app phone">{topbar_phone(title)}<main class="m-main">{body}</main></div>'


def chat(k, vp):
    c = COPY[k]
    you, jarvis = ("you", "jarvis") if k == "C" else ("Jordan", "Jarvis")

    def turn(role, inner, extra=""):
        who = you if role == "user" else jarvis
        return f'<div class="m-turn {role}"><span class="m-who">{who}</span><div class="m-msg">{inner}</div>{extra}</div>'

    thread = (
        '<div class="m-day">Today</div>'
        + turn("user", "Remember that the dog walker comes at five on weekdays")
        + turn("asst", f'<span class="m-tool">{icon("brain")}Saved to your memories</span><p>Got it. Mia, the dog walker, comes at 5 pm on weekdays.</p>')
        + turn("user", "And unlock the front door for her then")
        + turn("asst", '<p>I can do that every weekday at 5 pm. Unlocking a door needs your OK first:</p>'
               f'<div class="m-confirm"><div class="m-confirm-h">{icon("lock")}<b>Unlock Front door</b></div>'
               '<p class="m-muted">Weekdays at 17:00 · locks again after 10 minutes</p>'
               '<div class="m-row-actions"><button class="m-btn primary">Approve</button><button class="m-btn">Not now</button></div></div>')
        + turn("user", "What's the weather for her walk?")
        + turn("asst", '<p>Around 5 pm: <b>14°C</b>, light wind, 10% chance of rain. A good afternoon for it.</p>')
    )
    device = f'<button class="m-device"><span class="m-dot ok"></span>Kitchen{icon("chev")}</button>'
    head = (f'<div class="m-chathead"><h1 class="t-title">Chat</h1>{device}'
            f'<button class="m-btn ghost">{icon("plus")}<span>New</span></button></div>')
    composer = (f'<div class="m-composer"><div class="m-input"><button class="m-icbtn" aria-label="Attach a photo">{icon("clip")}</button>'
                f'<span class="m-ph">{c["chat_ph"]}</span><button class="m-icbtn" aria-label="Speak">{icon("mic")}</button>'
                f'<button class="m-send" aria-label="Send">{icon("send")}</button></div></div>')
    body = f'<div class="m-chat">{head}<div class="m-thread">{thread}</div>{composer}</div>'
    if vp == "desk":
        return f'<div class="m-app">{sidebar(k, "Chat")}<main class="m-main flush">{body}</main></div>'
    return f'<div class="m-app phone">{topbar_phone("Chat")}<main class="m-main flush">{body}</main></div>'


def token_css():
    out = []
    for k, d in DIRECTIONS.items():
        for theme in ("light", "dark"):
            sel = f".dir-{k}" + (".dark" if theme == "dark" else "")
            t = dict(d[theme])
            t.setdefault("accent", t["primary"])
            props = "".join(f"--{n}:{v};" for n, v in t.items())
            out.append(f"{sel}{{{props}}}")
        ty = "".join(f"--fs-{n}:{px}px;--lh-{n}:{lh};--fw-{n}:{w};" for n, px, lh, w, _ in d["type"])
        r = d["radius"]
        out.append(f".dir-{k}{{--font:{d['font']};--mono:{d['mono']};{ty}--r-ctl:{r['control']}px;--r-card:{r['card']}px;--r-bubble:{r['bubble']}px;}}")
    return "\n".join(out)


MOCK_CSS = r"""
.frame{font-family:var(--font);color:var(--text);background:var(--bg);overflow:hidden;position:relative;
  -webkit-font-smoothing:antialiased;font-size:var(--fs-body);line-height:var(--lh-body)}
.frame *{box-sizing:border-box}
.frame :where(p,h1,h2){margin:0}.frame :where(ul){list-style:none;margin:0;padding:0}
.frame :where(button){font:inherit;color:inherit;background:none;border:0;cursor:pointer}
.ic{width:18px;height:18px;flex:none}
.t-display{font-size:var(--fs-display);line-height:var(--lh-display);font-weight:var(--fw-display)}
.t-title{font-size:var(--fs-title);line-height:var(--lh-title);font-weight:var(--fw-title);letter-spacing:-.01em}
.t-heading{font-size:var(--fs-heading);line-height:var(--lh-heading);font-weight:var(--fw-heading);display:flex;gap:8px;align-items:center}
.t-heading .ic{color:var(--muted)}.t-heading .ic.warn{color:var(--warning)}
.m-muted{color:var(--muted);font-size:var(--fs-small);line-height:var(--lh-small)}
.m-num{font-family:var(--mono);font-variant-numeric:tabular-nums;font-size:var(--fs-small);white-space:nowrap}
.m-link{color:var(--accent);font-size:var(--fs-small);text-decoration:none;font-weight:500}
.m-app{display:flex;height:100%}
.m-app.phone{flex-direction:column}
.m-side{width:248px;flex:none;background:var(--surface);border-right:1px solid var(--border);display:flex;flex-direction:column;padding:16px 12px}
.m-brand{display:flex;align-items:center;gap:10px;font-weight:650;font-size:16px;padding:4px 8px 16px}
.m-mark{width:22px;height:22px;border-radius:calc(var(--r-ctl) * .75);background:var(--primary)}
.m-nav{display:flex;align-items:center;gap:12px;padding:8px 10px;border-radius:var(--r-ctl);color:var(--muted);font-size:var(--fs-small);font-weight:500;min-height:38px}
.m-nav[aria-current]{background:var(--primary-soft);color:var(--text);font-weight:600}
.m-nav[aria-current] .ic{color:var(--primary)}
.m-badge{margin-left:auto;font-size:var(--fs-caption);font-weight:600;min-width:20px;text-align:center;padding:1px 6px;border-radius:999px;background:var(--primary);color:var(--on-primary)}
.m-navgroup{font-size:var(--fs-caption);color:var(--muted);font-weight:600;padding:18px 10px 6px}
.m-me{margin-top:auto;display:flex;gap:10px;align-items:center;padding:12px 8px 0;border-top:1px solid var(--border)}
.m-me b{display:block;font-size:var(--fs-small)}.m-me small{color:var(--muted);font-size:var(--fs-caption)}
.m-avatar{width:32px;height:32px;border-radius:999px;background:var(--primary-soft);color:var(--text);display:grid;place-items:center;font-weight:600;font-size:14px;flex:none}
.m-avatar.sm{width:30px;height:30px}
.m-ptop{height:56px;flex:none;display:flex;align-items:center;gap:8px;padding:0 12px;background:var(--surface);border-bottom:1px solid var(--border)}
.m-ptitle{font-weight:600;font-size:17px;flex:1}
.m-icbtn{width:40px;height:40px;display:grid;place-items:center;border-radius:var(--r-ctl);color:var(--muted)}
.m-main{flex:1;overflow:hidden;min-width:0}
.m-page{max-width:1080px;margin:0 auto;padding:32px 40px}
.phone .m-page{padding:20px 16px}
.m-pagehead{margin-bottom:20px}.m-pagehead .m-muted{margin-top:2px}
.m-greet{margin:8px 0 24px}.m-greet-sub{color:var(--muted);font-size:var(--fs-heading);margin-top:4px}
.m-banner{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:14px 16px;border-radius:var(--r-card);background:var(--primary-soft);margin-bottom:20px;font-size:var(--fs-small)}
.m-banner span{color:var(--text)}
.m-btn{display:inline-flex;align-items:center;gap:6px;min-height:36px;padding:0 14px;border-radius:var(--r-ctl);border:1px solid var(--border-strong) !important;background:var(--surface) !important;font-weight:600;font-size:var(--fs-small);white-space:nowrap}
.m-btn.primary{background:var(--primary) !important;color:var(--on-primary) !important;border-color:var(--primary) !important}
.m-btn.ghost{border-color:transparent !important;background:transparent !important;color:var(--muted) !important}
.m-btn.sm{min-height:32px;padding:0 12px}
.phone .m-btn{min-height:40px}
.m-grid{display:grid;grid-template-columns:1.1fr 1fr;gap:20px;align-items:start}
.phone .m-grid{grid-template-columns:1fr;gap:16px}
.m-card{background:var(--surface);border:1px solid var(--border);border-radius:var(--r-card);padding:20px}
.phone .m-card{padding:16px}
.m-cardhead{display:flex;justify-content:space-between;align-items:center;margin-bottom:8px}
.m-attn .t-heading{margin-bottom:12px}
.m-issue{border-radius:calc(var(--r-card) - 6px);background:var(--warning-soft);padding:14px}
.m-issue-t{font-weight:600;margin-bottom:4px}
.m-code{display:flex;gap:8px;align-items:flex-start;margin:12px 0;background:var(--surface);border:1px solid var(--border);border-radius:var(--r-ctl);padding:8px 4px 8px 12px}
.m-code code{font-family:var(--mono);font-size:var(--fs-caption);line-height:1.5;flex:1;padding-top:3px;overflow-wrap:anywhere}
.m-code .m-icbtn{width:32px;height:32px}
.m-row-actions{display:flex;gap:12px;align-items:center;flex-wrap:wrap}
.m-ok{margin-top:12px}
.m-list li{display:flex;align-items:center;gap:12px;padding:10px 0;border-top:1px solid var(--border)}
.m-list li:first-child{border-top:0}
.m-li-main{flex:1;min-width:0;display:flex;flex-direction:column}
.m-li-main small{color:var(--muted);font-size:var(--fs-small)}
.m-dot{width:8px;height:8px;border-radius:999px;background:var(--border-strong);flex:none}
.m-dot.ok{background:var(--success)}
.m-chip{font-size:var(--fs-caption);font-weight:600;padding:2px 10px;border-radius:999px;background:var(--surface-alt);color:var(--muted);white-space:nowrap}
.m-chip.ok{background:var(--success-soft);color:var(--success)}
.m-chip.bad{background:var(--danger-soft);color:var(--danger)}
.m-col{display:flex;flex-direction:column;gap:inherit;min-width:0}
.m-kv{display:grid;grid-template-columns:auto 1fr;gap:8px 16px;margin:4px 0 0;font-size:var(--fs-small)}
.m-kv dt{color:var(--muted)}.m-kv dd{margin:0;font-family:var(--mono);font-size:var(--fs-caption);padding-top:2px}
/* chat */
.m-main.flush{display:flex}
.m-chat{flex:1;display:flex;flex-direction:column;min-width:0;height:100%}
.m-chathead{display:flex;align-items:center;gap:12px;padding:16px 40px;border-bottom:1px solid var(--border);background:var(--bg)}
.phone .m-chathead{padding:10px 16px}.phone .m-chathead .t-title{display:none}
.m-device{display:inline-flex;align-items:center;gap:8px;min-height:36px;padding:0 10px 0 12px;border-radius:999px;border:1px solid var(--border-strong) !important;font-size:var(--fs-small);font-weight:500}
.m-device .ic{width:16px;height:16px;color:var(--muted)}
.m-chathead .m-btn.ghost{margin-left:auto}
.m-thread{flex:1;overflow:hidden;padding:24px 40px;display:flex;flex-direction:column;justify-content:flex-end;gap:18px;max-width:800px;width:100%;margin:0 auto}
.phone .m-thread{padding:16px;gap:14px}
.m-day{align-self:center;font-size:var(--fs-caption);color:var(--muted);font-weight:500}
.m-turn{display:flex;flex-direction:column;gap:4px;max-width:85%}
.m-turn.user{align-self:flex-end;align-items:flex-end}
.m-who{font-size:var(--fs-caption);color:var(--muted);font-weight:500}
.m-msg{padding:10px 14px;border-radius:var(--r-bubble);display:flex;flex-direction:column;gap:8px}
.m-turn.user .m-msg{background:var(--primary);color:var(--on-primary);border-bottom-right-radius:6px}
.m-turn.asst .m-msg{background:var(--surface);border:1px solid var(--border);border-bottom-left-radius:6px}
.m-tool{display:inline-flex;align-items:center;gap:6px;font-size:var(--fs-caption);font-weight:600;color:var(--success);background:var(--success-soft);padding:3px 10px;border-radius:999px;align-self:flex-start}
.m-tool .ic{width:14px;height:14px}
.m-confirm{border:1px solid var(--border-strong);border-radius:calc(var(--r-bubble) - 8px);padding:12px 14px;background:var(--bg);display:flex;flex-direction:column;gap:6px}
.m-confirm-h{display:flex;gap:8px;align-items:center}.m-confirm-h .ic{color:var(--warning)}
.m-composer{padding:12px 40px 20px;max-width:800px;width:100%;margin:0 auto}
.phone .m-composer{padding:8px 12px 12px}
.m-input{display:flex;align-items:center;gap:4px;min-height:52px;padding:6px;border:1px solid var(--border-strong);border-radius:calc(var(--r-bubble) + 2px);background:var(--surface)}
.m-ph{flex:1;color:var(--muted);padding:0 6px}
.m-send{width:40px;height:40px;border-radius:calc(var(--r-bubble) - 4px) !important;background:var(--primary) !important;color:var(--on-primary) !important;display:grid;place-items:center}
.m-send .ic{width:18px;height:18px}

/* A: Hearth. Roomy and soft; the assistant speaks without a bubble; user bubbles are a tint, not a fill. */
.dir-A .m-side{background:var(--bg);border-right-color:transparent}
.dir-A .m-nav{font-size:15px;min-height:44px}
.dir-A .m-card{border-color:transparent;box-shadow:0 1px 2px rgb(60 40 20 / .06),0 4px 16px rgb(60 40 20 / .05)}
.dir-A.dark .m-card{box-shadow:none;border-color:var(--border)}
.dir-A .m-page{padding:40px 48px}.dir-A .phone .m-page,.dir-A .m-app.phone .m-page{padding:20px}
.dir-A .m-grid{gap:24px}
.dir-A .m-banner{background:var(--surface);border:1px dashed var(--border-strong)}
.dir-A .m-turn.user .m-msg{background:var(--primary-soft);color:var(--text)}
.dir-A .m-turn.asst .m-msg{background:transparent;border:0;padding:2px 0}
.dir-A .m-turn.asst{max-width:92%}
.dir-A .m-msg{font-size:16px}
.dir-A .m-confirm{background:var(--surface);border-color:var(--border)}
.dir-A .m-chathead{border-bottom-color:transparent}
.dir-A .m-mark{border-radius:999px}

/* B: Quiet Violet. The current layout, made consistent: one card style, one chip style, status tokens. */
.dir-B .m-banner{border:1px solid color-mix(in srgb,var(--primary) 25%,transparent)}

/* C: Ledger. No cards: sections under a rule; data in mono; transcript instead of bubbles. */
.dir-C .m-side{padding:12px 8px}
.dir-C .m-nav{min-height:32px;padding:6px 10px}
.dir-C .m-nav[aria-current]{background:var(--surface-alt)}
.dir-C .m-nav[aria-current] .ic{color:var(--text)}
.dir-C .m-badge{background:var(--surface-alt);color:var(--text);border:1px solid var(--border-strong)}
.dir-C .m-mark{background:var(--text);border-radius:3px}
.dir-C .m-card{border:0;border-top:1px solid var(--border-strong);border-radius:0;background:transparent;padding:14px 0 0}
.dir-C .m-grid{gap:28px 40px}
.dir-C .phone .m-grid,.dir-C .m-app.phone .m-grid{gap:24px}
.dir-C .t-heading .ic{display:none}
.dir-C .m-list li{padding:7px 0}
.dir-C .m-li-main{flex-direction:row;gap:12px;align-items:baseline}
.dir-C .m-li-main small{margin-left:auto;font-family:var(--mono);font-size:12px}
.dir-C .m-app.phone .m-li-main{flex-direction:column;gap:0}.dir-C .m-app.phone .m-li-main small{margin-left:0}
.dir-C .m-chip{border-radius:3px;padding:1px 6px;font-family:var(--mono);font-weight:500;text-transform:lowercase}
.dir-C .m-issue{background:transparent;border-left:0;padding:0}
.dir-C .m-issue-t{color:var(--warning)}
.dir-C .m-banner{background:transparent;border:1px solid var(--border-strong);border-radius:var(--r-card)}
.dir-C .m-page{padding:28px 40px}.dir-C .m-app.phone .m-page{padding:16px}
.dir-C .m-turn{max-width:100%;flex-direction:row;gap:16px;align-self:stretch !important;align-items:flex-start !important;padding-top:14px;border-top:1px solid var(--border)}
.dir-C .m-day{align-self:flex-start;font-family:var(--mono)}
.dir-C .m-who{width:56px;flex:none;font-family:var(--mono);padding-top:1px}
.dir-C .m-turn.user .m-who{color:var(--text)}
.dir-C .m-turn.asst .m-who{color:var(--accent)}
.dir-C .m-msg,.dir-C .m-turn.user .m-msg,.dir-C .m-turn.asst .m-msg{background:transparent;color:var(--text);border:0;padding:0;border-radius:0}
.dir-C .m-thread{gap:14px}
.dir-C .m-tool{background:transparent;padding:0;font-family:var(--mono);font-weight:500}
.dir-C .m-device{border-radius:var(--r-ctl)}
.dir-C .m-input{border-radius:var(--r-card)}
.dir-C .m-send{border-radius:var(--r-ctl) !important}
"""

PAGE_CSS = r"""
:root{color-scheme:light;--ink:#1d1d1f;--sub:#5d5d66;--line:#e3e3e8;--paper:#fafafa}
*{box-sizing:border-box}
body{margin:0;font:15px/1.55 system-ui,-apple-system,'Segoe UI',Roboto,sans-serif;color:var(--ink);background:var(--paper)}
.wrap{max-width:1400px;margin:0 auto;padding:32px 24px 80px}
h1{font-size:28px;margin:0 0 4px}h2{font-size:22px;margin:0}h3{font-size:15px;margin:20px 0 8px}
p{margin:0 0 10px;max-width:75ch}
.sub{color:var(--sub)}
.toolbar{position:sticky;top:0;z-index:5;background:color-mix(in srgb,var(--paper) 92%,transparent);backdrop-filter:blur(6px);border-bottom:1px solid var(--line);padding:10px 24px;display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.toolbar button{font:inherit;padding:6px 14px;border-radius:8px;border:1px solid #9a9aa3;background:#fff;cursor:pointer}
.toolbar button[aria-pressed=true]{background:var(--ink);color:#fff;border-color:var(--ink)}
.toolbar button:focus-visible{outline:2px solid #2563eb;outline-offset:2px}
.toolbar a{color:var(--ink);font-size:14px;margin-left:8px}
table{border-collapse:collapse;width:100%;font-size:14px}
th,td{text-align:left;padding:6px 10px;border-top:1px solid var(--line);vertical-align:top}
th{font-weight:600;background:#f1f1f4}
.summary td:first-child{font-weight:600;white-space:nowrap}
.dir{margin-top:48px;padding-top:24px;border-top:2px solid var(--ink)}
.dir header{display:flex;gap:16px;align-items:baseline;flex-wrap:wrap}
.letter{font:700 14px/1 ui-monospace,monospace;background:var(--ink);color:#fff;padding:6px 9px;border-radius:6px}
.note{padding:8px 12px;border-radius:8px;background:#fff3d6;color:#5a3b00;display:inline-block;font-size:14px;margin:6px 0 0}
.note.keep{background:#e8f5ec;color:#14532d}
.cols{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:24px;margin-top:16px}
.sw{display:grid;grid-template-columns:repeat(auto-fill,minmax(112px,1fr));gap:6px}
.sw div{border-radius:8px;padding:8px;font:12px/1.3 ui-monospace,monospace;min-height:58px;border:1px solid rgb(0 0 0 / .08)}
.spec{border-radius:10px;padding:16px;border:1px solid var(--line)}
.spec div{display:flex;gap:12px;align-items:baseline;border-top:1px dashed rgb(128 128 128 / .3);padding:4px 0}
.spec small{font:11px ui-monospace,monospace;opacity:.75;width:150px;flex:none}
.scale{display:flex;gap:6px;align-items:flex-end;flex-wrap:wrap}
.scale span{background:#d9d9e0;display:inline-block}
.radii{display:flex;gap:10px}
.radii span{width:56px;height:40px;border:2px solid var(--ink);display:grid;place-items:center;font:11px ui-monospace,monospace}
ul.motion{margin:0;padding-left:18px}ul.motion li{margin:3px 0}
.mocks{display:grid;grid-template-columns:3fr 1fr;gap:16px;margin-top:16px;align-items:start}
.mock{border:1px solid var(--line);border-radius:10px;overflow:hidden;background:#fff}
.mock figcaption{font-size:13px;color:var(--sub);padding:6px 10px;border-top:1px solid var(--line)}
.viewport{position:relative;overflow:hidden}
.viewport .frame{position:absolute;top:0;left:0;transform-origin:0 0}
details{margin-top:12px}summary{cursor:pointer;font-weight:600}
.pass{color:#166534}.fail{color:#b91c1c;font-weight:700}
@media (max-width:760px){.mocks{grid-template-columns:1fr}.wrap{padding:20px 14px 60px}}
body.single{background:#fff}body.single .frame{position:static}
"""

JS = r"""
const q=new URLSearchParams(location.search);
function setTheme(t){document.querySelectorAll('.frame').forEach(f=>f.classList.toggle('dark',t==='dark'));
  document.querySelectorAll('[data-theme-btn]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.themeBtn===t)));}
function fit(){document.querySelectorAll('.viewport').forEach(v=>{const f=v.firstElementChild;const w=+f.dataset.w,h=+f.dataset.h;
  const s=v.clientWidth/w;f.style.transform='scale('+s+')';v.style.height=(h*s)+'px';});}
if(q.get('frame')){const id=q.get('frame');const f=document.getElementById(id);document.body.className='single';
  document.body.replaceChildren(f);f.classList.toggle('dark',q.get('theme')==='dark');}
else{document.querySelectorAll('[data-theme-btn]').forEach(b=>b.addEventListener('click',()=>setTheme(b.dataset.themeBtn)));
  setTheme(q.get('theme')||'light');fit();addEventListener('resize',fit);}
"""

SIZES = {"desk": (1440, 900), "phone": (390, 844)}


def frame(k, page, vp):
    w, h = SIZES[vp]
    inner = dashboard(k, vp) if page == "dash" else chat(k, vp)
    fid = f"{k}-{page}-{vp}"
    return (f'<div class="viewport"><div class="frame dir-{k}" id="{fid}" data-w="{w}" data-h="{h}" '
            f'style="width:{w}px;height:{h}px">{inner}</div></div>')


def swatches(d, theme):
    t = d[theme]
    cells = []
    for name, v in t.items():
        fg = d[theme]["text"] if name not in ("primary",) else d[theme]["on-primary"]
        if name in ("text", "muted", "success", "warning", "danger", "border-strong", "focus", "on-primary"):
            fg = d[theme]["bg"] if name != "on-primary" else d[theme]["primary"]
        cells.append(f'<div style="background:{v};color:{fg}">{name}<br>{v}</div>')
    return f'<div class="sw">{"".join(cells)}</div>'


def spec(d, theme):
    t = d[theme]
    rows = "".join(f'<div><small>{n} {px}/{lh} {w}</small><span style="font-size:{px}px;line-height:{lh};font-weight:{w}">{esc(use)}</span></div>'
                   for n, px, lh, w, use in d["type"])
    mono = f'<div><small>mono</small><span style="font-family:{d["mono"]};font-size:13px">412 ms · 192.168.1.0/24 · 5eed0000</span></div>'
    return f'<div class="spec" style="font-family:{d["font"]};background:{t["surface"]};color:{t["text"]}">{rows}{mono}</div>'


def contrast_table(d):
    rows = []
    for theme, label, fg, bg, r, need in checks(d):
        ok = r >= need
        rows.append(f'<tr><td>{theme}</td><td>{esc(label)}</td><td><code>{fg}</code> on <code>{bg}</code></td>'
                    f'<td>{r:.2f}</td><td>{need}</td><td class="{"pass" if ok else "fail"}">{"pass" if ok else "FAIL"}</td></tr>')
    return ('<table><tr><th>Theme</th><th>Pair</th><th>Colours</th><th>Ratio</th><th>Needs</th><th></th></tr>'
            + "".join(rows) + "</table>")


def section(k, d):
    keep = "keep" if d["keeps_purple"] else ""
    sp = "".join(f'<span style="width:{s}px;height:{s}px" title="{s}px"></span>' for s in d["space"])
    radii = "".join(f'<span style="border-radius:{min(v, 20)}px">{n}<br>{v if v < 999 else "full"}</span>' for n, v in d["radius"].items())
    motion = "".join(f"<li>{esc(m)}</li>" for m in d["motion"])
    n_fail = sum(1 for c in checks(d) if c[4] < c[5])
    return f"""
<section class="dir" id="dir-{k}">
<header><span class="letter">{k}</span><h2>{d['name']}</h2><span class="sub">{esc(d['tagline'])}</span></header>
<p class="note {keep}">{esc(d['primary_note'])}</p>
<div class="mocks">
  <figure class="mock" style="margin:0">{frame(k, 'dash', 'desk')}<figcaption>Dashboard, desktop 1440</figcaption></figure>
  <figure class="mock" style="margin:0">{frame(k, 'dash', 'phone')}<figcaption>Dashboard, phone 390</figcaption></figure>
  <figure class="mock" style="margin:0">{frame(k, 'chat', 'desk')}<figcaption>Chat, desktop 1440</figcaption></figure>
  <figure class="mock" style="margin:0">{frame(k, 'chat', 'phone')}<figcaption>Chat, phone 390</figcaption></figure>
</div>
<div class="cols">
  <div><h3>Type (font: <code>{esc(d['font'].split(',')[0])}</code>, system stack)</h3>{spec(d, 'light')}</div>
  <div><h3>Spacing (px)</h3><div class="scale">{sp}</div><p class="sub" style="margin-top:6px">{' · '.join(map(str, d['space']))}. {esc(d['density'])}</p>
       <h3>Radius</h3><div class="radii">{radii}</div></div>
  <div><h3>Motion</h3><ul class="motion">{motion}</ul>
       <p class="sub" style="margin-top:6px">Curves (Emil): <code>--ease-out: cubic-bezier(0.23,1,0.32,1)</code>, <code>--ease-drawer: cubic-bezier(0.32,0.72,0,1)</code>. Nothing over 300 ms; no animation library.</p></div>
</div>
<div class="cols">
  <div><h3>Colour, light</h3>{swatches(d, 'light')}</div>
  <div><h3>Colour, dark</h3>{swatches(d, 'dark')}</div>
</div>
<details><summary>Contrast: {len(checks(d))} pairs, {n_fail} failing</summary>{contrast_table(d)}</details>
</section>"""


def summary():
    rows = [
        ("Feel", "Warm, soft, domestic", "Familiar, tidy, neutral", "Quiet, exact, tool-like"),
        ("Brand colour", "Clay #a8441f (changes the purple)", "Purple #8A00C4 kept", "Ink + teal #0b6e66 (changes the purple)"),
        ("Neutrals", "Warm stone", "Neutral grey (slight cool)", "Paper white / graphite"),
        ("Type", "ui-rounded, 16px body, 1.25 scale", "system-ui, 15px body, 1.2 scale", "system-ui + mono data, 14px body"),
        ("Shape", "Radius 12 / 18, soft shadow (light)", "Radius 8 / 12, borders", "Radius 4 / 6, rules, no cards"),
        ("Density", "Roomy (48px rows)", "Daily app (44px rows)", "Dense (36px rows, 44 on touch)"),
        ("Chat", "Assistant without bubble, tinted user bubble", "Two bubbles, filled user bubble", "Transcript, no bubbles"),
        ("Motion", "Most: press scale, newest message fades in", "Some: press scale, menus, drawer", "Least: drawer and menu opacity only"),
        ("Migration", "New palette + shape: every page", "Tokens + components; layouts mostly stay", "New palette + layout: dashboard-like pages rework"),
    ]
    body = "".join(f"<tr>{''.join(f'<td>{esc(c)}</td>' for c in r)}</tr>" for r in rows)
    return (f'<table class="summary"><tr><th></th><th>A · Hearth</th><th>B · Quiet Violet</th><th>C · Ledger</th></tr>{body}</table>')


def page():
    secs = "".join(section(k, d) for k, d in DIRECTIONS.items())
    return f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Jarvis Automation: three visual directions (#331)</title>
<style>{PAGE_CSS}{token_css()}{MOCK_CSS}</style></head>
<body>
<div class="toolbar"><b>Mockups:</b><button data-theme-btn="light" aria-pressed="true">Light</button><button data-theme-btn="dark" aria-pressed="false">Dark</button>
<a href="#dir-A">A · Hearth</a><a href="#dir-B">B · Quiet Violet</a><a href="#dir-C">C · Ledger</a></div>
<div class="wrap">
<h1>Jarvis Automation: three visual directions</h1>
<p class="sub">For the design audit (docs/design/AUDIT-2026-10.md, item #331). Static mockups of Dashboard and Chat with the seeded household's people and rooms; the content is the same in all three so only the design differs. Every colour pair is checked against WCAG AA in both themes (the tables under each direction). System fonts only; this page fetches nothing.</p>
<p class="sub">All three fix the audit's systemic problems the same way: one token set with status colours, shared Button / Card / Chip / Field components, focus rings, skeleton and error states, and the motion rules below. They differ in feel, colour, shape and density. Pick one, or a mix (for example B's colours with C's dashboard layout).</p>
{summary()}
{secs}
</div>
<script>{JS}</script>
</body></html>"""


def contrast_md():
    lines = ["# Contrast checks for the three directions", "",
             "Generated by `build.py` from `tokens.py` (WCAG 2.x relative luminance). Text pairs need 4.5:1; input edges and focus rings 3:1 (WCAG 1.4.11).", ""]
    for k, d in DIRECTIONS.items():
        lines += [f"## {k} · {d['name']}", "", "| Theme | Pair | Colours | Ratio | Needs | |", "|---|---|---|---|---|---|"]
        for theme, label, fg, bg, r, need in checks(d):
            lines.append(f"| {theme} | {label} | `{fg}` on `{bg}` | {r:.2f} | {need} | {'pass' if r >= need else '**FAIL**'} |")
        lines.append("")
    return "\n".join(lines)


if __name__ == "__main__":
    with open(os.path.join(HERE, "directions.html"), "w") as f:
        f.write(page())
    with open(os.path.join(HERE, "contrast.md"), "w") as f:
        f.write(contrast_md())
    print("wrote directions.html, contrast.md")

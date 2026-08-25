# Design notes

Rewritten on 2026-08-25 after the second design pass. The first version of this
file handed off a drawn-timeline design; that design is gone, and a document
describing it would now mislead. What follows is the current state, the
decisions behind it, and what is still open.

## Where it is

| | |
|---|---|
| Live | `https://punchcard.cobanov.run` (landing) · `/app` (application) |
| Landing | `web/landing.html`, `web/src/landing.css`, `web/src/landing.ts` |
| Application | `web/src/App.tsx`, `web/src/components/*`, `web/src/styles.css` |
| Build | `make web` → `internal/http/webui/dist`, embedded in the binary |
| Deploy | `./scratchpad/deploy.sh <version>` |

React 19 + Vite 7 + Tailwind v4, two Vite entries (`app`, `landing`). The
landing page does not import the application bundle — it paints before any
JavaScript arrives.

> **The trap:** the binary serves the EMBEDDED `dist`, not `web/dist`. After a
> change run `make web` **and restart**. Verify with the served asset hash:
> `curl -s localhost:8080/app | grep -o 'assets/app-[^"]*\.js'`.

## The brief the app is built to

Fast, unconfusing, gone in seconds: open, press start, glance at a number, get
back to work. The design language is flat zinc — shadcn's dialect — dense 13px
rows, hairline borders, no shadows, pill tabs, one white primary button.

## The decisions

**Dark only.** A decision, not a default. The landing page is dark because its
visual idea is light through punched holes; the app is dark so the product does
not change identity on the way through its own front door. A light theme
belongs in settings, chosen by a person.

**Amber means time, and nothing else.** The running clock and dot, commit
counts and shas, the recovery affordance, focus rings. Never buttons, never
chrome. This is the one thread tying the app to the landing page, and it only
works because it is scarce.

**IBM Plex Sans + Mono, bundled.** IBM made the punch card; the typeface is the
subject's own. Bundling keeps a self-hosted instance self-contained.

**The day is rows, newest first.** The drawn timeline was the first design's
big swing and it lost on the terms that matter here: rows collided, short
sessions were unreadable, a normal day looked like an empty chart. A tool you
open for ten seconds cannot ask to be interpreted. The timeline's *vocabulary*
survives — amber evidence on sessions, hollow-marked recovery cards for work no
timer covered — without its geometry.

**Stats are a glance, not a visit.** today · week · commits sit under the timer
bar on the main screen. Reports keeps ranges (7/30 days), amounts and CSV, and
fetches its own data so the main screen never pays for it.

**Everything edits where it is displayed.** The rate is a click-to-edit cell
(Enter saves, Escape leaves, empty clears — "not costed" is not "costed at
zero"). Repositories live behind an expandable project row. `window.prompt()`
is gone.

**Durations say `6h 12m` everywhere.** The landing already spoke h/m while the
app said `6s 12d` — one number, two vocabularies.

## Still open

1. **No command menu.** `n` focuses the note input and Escape blurs; a tool
   this shape wants ⌘K eventually.
2. **Mobile has had no deliberate pass.** The layout wraps sanely (flex +
   truncation) but nothing below 640px has been designed.
3. **Sessions cannot be edited from the web.** The API supports time
   corrections, split and delete; no client exposes them yet.
4. **The landing page** kept its own richer style (the drawn card hero) — it
   was liked, it stays. If the app's zinc and the landing ever feel like two
   products, the landing moves toward the app, not the other way.

## The audit

`.agents/skills/redesign-existing-projects` is installed in this repo and is
the checklist this pass was measured against.

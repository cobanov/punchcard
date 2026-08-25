# Design handoff

Written on 2026-08-25, when the design was handed to someone else to take over.
It says what is there, why, and what is wrong with it — so none of that has to
be rediscovered by reading the CSS.

Nothing here is binding. It is a record of decisions, including the ones that
turned out badly, so the next pass can overturn them deliberately rather than by
accident.

## Where it is

| | |
|---|---|
| Live | `https://punchcard.cobanov.run` (landing) · `/app` (application) |
| Landing | `web/landing.html`, `web/src/landing.css`, `web/src/landing.ts` |
| Application | `web/src/App.tsx`, `web/src/components/*`, `web/src/styles.css` |
| Build | `make web` → `internal/http/webui/dist`, embedded in the binary |
| Deploy | `./scratchpad/deploy.sh <version>` |

React 19 + Vite 7 + Tailwind v4. Two Vite entries: `app` and `landing`. The
landing page does not import the application bundle — it ships its own
stylesheet and about sixty lines of script, because it is what a stranger sees
first and it has to paint before any JavaScript arrives.

> **The trap:** the binary serves the EMBEDDED `dist`, not `web/dist`. After a
> change run `make web` **and restart**. Verify with the served asset hash:
> `curl -s localhost:8080/app | grep -o 'assets/app-[^"]*\.js'`.

## The decisions, and why

**Dark only.** Not a default — a decision, and a late one. The app followed the
system at first, so signing in from the dark landing page turned the product
white and it changed identity walking through its own front door. A light theme
belongs in settings, chosen by a person, not switched on by a laptop.

**IBM Plex Sans and Mono, bundled.** IBM made the punch card, so the typeface is
the subject's own rather than a neutral pick. Bundling instead of linking a font
host keeps a self-hosted instance self-contained.

**Amber means time, and nothing else.** It appears on the running clock, on
commits, on the running band, on focus. Not on buttons — solid buttons are the
highest-contrast neutral. An accent that appears everywhere means nothing, and
amber on a light button was muddy brown besides.

**Every neutral is cool-tinted, from one family.** The first light palette put a
warm accent on a warm ground and landed exactly on the cream-and-terracotta look
every generated page arrives in.

**The day is a punch card, not a list.** Sessions are bands on an hour scale;
commits are punches inside them; a commit no session covers is a hollow punch in
empty space. Every other tracker lists the day, which answers "what did I do".
This answers the second question the product exists for — which stretches have
evidence and which do not — and makes the recovery feature something you see
rather than a section heading. It is the one real risk in the design.

**The landing hero draws the card rather than photographing anything.** 80
columns by 12 rows, laid back in perspective. The punched columns spell the
product's own sentence in IBM 029 keypunch encoding. The light through those
holes is where the accent colour comes from, so the palette is a consequence of
the image rather than a brand choice.

## What is wrong with it

Known, unfixed, and worth fixing:

1. **Overlapping rows in the day card.** Two sessions close in time draw on top
   of each other — the labels and the commit rows collide. Visible on the live
   app right now around 12:00. The card positions rows absolutely by clock time
   with no collision handling at all.
2. **A sparse day looks sparse.** Most days have two or three sessions, and the
   card is mostly empty rail. The panel helped; it is not solved.
3. **Very short sessions are unreadable.** A ten-minute session is a 6px band
   with a full label overflowing it.
4. **Projects and Reports had no design pass.** They are functional lists that
   inherited the tokens. The Projects screen in particular still uses `prompt()`
   to edit a rate, which is a placeholder, not a decision.
5. **No empty state for Reports**, and the loading state is a bare word rather
   than the skeleton the stylesheet defines.
6. **No keyboard surface beyond `n`.** A tool this shape wants a command menu.
7. **Mobile is untested.** The layout is responsive in the trivial sense and
   nothing has been looked at below 640px.

## The audit this was measured against

`.agents/skills/redesign-existing-projects` — installed in this repo. The first
version of this design failed most of it (airy where it should be dense, warm on
warm, no texture, no press feedback, no composed empty states, no favicon or
social meta). Running it again on the current state is a reasonable place to
start.

Also installed and unread: `minimalist-ui`, `high-end-visual-design`,
`industrial-brutalist-ui`, `stitch-design-taste`, `brandkit`.

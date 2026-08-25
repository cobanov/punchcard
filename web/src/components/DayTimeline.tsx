import { useEffect, useRef, useState } from "react";
import type { Project, Session } from "../lib/api";
import { assignColors } from "../lib/palette";
import { hhmm, total } from "../lib/format";

/**
 * The day as a shape.
 *
 * The table below says what happened and for how long; it does not say *when*,
 * or how the day was broken up. Twenty-four hours of strip with three blocks in
 * it answers a question no list answers: whether the day was one stretch or six,
 * whether the gaps are lunch or an afternoon that got away.
 *
 * The full day is always drawn, never trimmed to the hours that have work in
 * them. An empty morning is information, and a strip that rescales itself every
 * day cannot be compared with yesterday's.
 */

const MINUTES = 24 * 60;

/** Minutes past local midnight. The table beside this reads clock times in the
 *  browser's zone, so this does too — two clocks on one screen is worse than a
 *  clock that disagrees with a report cut elsewhere. */
function minuteOfDay(iso: string): number {
  const d = new Date(iso);
  return d.getHours() * 60 + d.getMinutes();
}

const pct = (minutes: number) => (minutes / MINUTES) * 100;

/**
 * The hours are marked at three densities, because one density can only answer
 * one question. Every hour, barely visible, gives the eye something to measure
 * a short block against. Every six, brighter and labelled, are the landmarks
 * you actually navigate by — morning, noon, evening. And the hours before 07:00
 * and after 19:00 sit on a darker ground, which is what turns a ruler into a
 * day: you can see the shape of when you work without reading a single number.
 */
const NIGHT = [
  [0, 7 * 60],
  [19 * 60, MINUTES],
];
const LANDMARKS = [0, 6, 12, 18, 24];

export function DayTimeline({
  sessions,
  projects,
}: {
  sessions: Session[];
  projects: Project[];
}) {
  // The running block grows about a fifteenth of a percent per minute across a
  // strip this wide, so it ticks once a minute rather than once a second. The
  // clock in the timer bar is the thing that has to move; this is a shape.
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 60_000);
    return () => clearInterval(id);
  }, []);

  const [hovered, setHovered] = useState<number | null>(null);
  // A tooltip that animates every time you cross a block turns scrubbing along
  // the strip into a flicker of little pops. The first one in a burst animates;
  // while the pointer keeps moving between blocks, the rest just move.
  const warmUntil = useRef(0);
  const warm = hovered !== null && Date.now() < warmUntil.current;
  useEffect(() => {
    if (hovered === null) warmUntil.current = Date.now() + 400;
  }, [hovered]);

  const colors = assignColors(projects);
  const name = (id: string) => projects.find((p) => p.id === id)?.name ?? "—";

  const blocks = sessions
    .map((s) => {
      const from = minuteOfDay(s.started_at);
      // A running session is drawn up to this moment; a finished one to its end.
      // Anything that crosses midnight is clipped at the edge of the day rather
      // than wrapped, because the row it belongs to is on the other day's list.
      const to = s.ended_at ? minuteOfDay(s.ended_at) : now.getHours() * 60 + now.getMinutes();
      const end = to < from ? MINUTES : to;
      return { s, from, end, running: !s.ended_at };
    })
    .filter((b) => b.end >= b.from);

  if (!blocks.length) return null;

  const active = hovered === null ? null : blocks[hovered];

  return (
    <div className="border-b border-line px-3 py-2.5">
      <div className="relative h-9 overflow-hidden rounded-md bg-ink">
        {NIGHT.map(([from, to]) => (
          <span
            key={from}
            className="absolute inset-y-0 bg-black/35"
            style={{ left: `${pct(from!)}%`, width: `${pct(to! - from!)}%` }}
            aria-hidden
          />
        ))}

        {Array.from({ length: 23 }, (_, i) => i + 1).map((h) => (
          <span
            key={h}
            className={h % 6 === 0 ? "absolute inset-y-0 w-px bg-line-strong" : "absolute inset-y-0 w-px bg-line/60"}
            style={{ left: `${pct(h * 60)}%` }}
            aria-hidden
          />
        ))}

        {blocks.map((b, i) => {
          // A two-minute session is still a fact about the day. Give every block
          // a floor so it cannot round away to nothing.
          const width = Math.max(pct(b.end - b.from), 0.35);
          return (
            <span
              key={`${b.s.id}-${i}`}
              className={b.running ? "bar-rise breathe absolute inset-y-1 rounded-[3px]" : "bar-rise absolute inset-y-1 rounded-[3px]"}
              // Capped so a busy day does not spend a second dealing itself out;
              // past six blocks the stagger has already done its job.
              style={{
                left: `${pct(b.from)}%`,
                width: `${width}%`,
                animationDelay: `${Math.min(i * 40, 240)}ms`,
                // Running time is amber, everywhere in this app. A finished
                // stretch wears its project's colour.
                background: b.running ? "var(--color-punch)" : colors.get(b.s.project_id),
                opacity: b.running ? 1 : 0.85,
              }}
              onMouseEnter={() => setHovered(i)}
              onMouseLeave={() => setHovered((h) => (h === i ? null : h))}
            />
          );
        })}
      </div>

      <div className="relative mt-1 h-4">
        {LANDMARKS.map((h) => (
          <span
            key={h}
            className="absolute t-caption text-faint"
            // Positioned at the hour it names rather than spread with
            // justify-between, which puts each label near its mark instead of
            // on it. The ends pull inward by their own width so neither hangs
            // off the strip.
            style={{
              left: `${pct(h * 60)}%`,
              transform: h === 0 ? "none" : h === 24 ? "translateX(-100%)" : "translateX(-50%)",
            }}
            aria-hidden
          >
            {String(h).padStart(2, "0")}
          </span>
        ))}

        {active && (
          <span
            role="status"
            className={
              warm
                ? "tip tip-instant absolute z-10 -top-0.5 whitespace-nowrap rounded-md border border-line-strong bg-raise px-2 py-1 t-caption text-text shadow-lg"
                : "tip absolute z-10 -top-0.5 whitespace-nowrap rounded-md border border-line-strong bg-raise px-2 py-1 t-caption text-text shadow-lg"
            }
            style={{
              left: `${Math.min(Math.max(pct((active.from + active.end) / 2), 6), 94)}%`,
              transform: "translateX(-50%)",
            }}
          >
            <span
              className="mr-1.5 inline-block size-1.5 rounded-full align-middle"
              style={{
                background: active.running ? "var(--color-punch)" : colors.get(active.s.project_id),
              }}
              aria-hidden
            />
            {name(active.s.project_id)}
            <span className="ml-1.5 tnum font-mono text-dim">
              {hhmm(active.s.started_at)}
              {active.s.ended_at ? `–${hhmm(active.s.ended_at)}` : "–now"}
            </span>
            <span className="ml-1.5 tnum text-faint">{total(active.s.seconds)}</span>
          </span>
        )}
      </div>
    </div>
  );
}

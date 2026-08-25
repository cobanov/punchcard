import { useEffect, useState } from "react";
import type { Project, Session } from "../lib/api";
import { assignColors } from "../lib/palette";
import { total } from "../lib/format";

/**
 * The day as a shape.
 *
 * The table below says what happened and for how long; it does not say *when*,
 * or how the day was broken up. Twenty-four hours of blank strip with three
 * blocks in it answers a question no list answers: whether the day was one
 * stretch or six, whether the gaps are lunch or an afternoon that got away.
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

  const colors = assignColors(projects);
  const name = (id: string) => projects.find((p) => p.id === id)?.name ?? "—";

  const blocks = sessions
    .map((s) => {
      const from = minuteOfDay(s.started_at);
      // A running session is drawn up to this moment; a finished one to its end.
      // Anything that crosses midnight is clipped at the edge of the day rather
      // than wrapped, because the row it belongs to is on the other day's list.
      const to = s.ended_at
        ? minuteOfDay(s.ended_at)
        : now.getHours() * 60 + now.getMinutes();
      const end = to < from ? MINUTES : to;
      return { s, from, end, running: !s.ended_at };
    })
    .filter((b) => b.end >= b.from);

  if (!blocks.length) return null;

  return (
    <div className="border-b border-line px-3 py-2.5">
      <div className="relative h-9 overflow-hidden rounded-md bg-ink">
        {/* A tick every three hours. Enough to place a block without turning the
            strip into graph paper. */}
        {[3, 6, 9, 12, 15, 18, 21].map((h) => (
          <span
            key={h}
            className="absolute inset-y-0 w-px bg-line"
            style={{ left: `${((h * 60) / MINUTES) * 100}%` }}
            aria-hidden
          />
        ))}

        {blocks.map((b, i) => {
          const left = (b.from / MINUTES) * 100;
          // A two-minute session is still a fact about the day. Give every block
          // a floor so it cannot round away to nothing.
          const width = Math.max(((b.end - b.from) / MINUTES) * 100, 0.35);
          return (
            <span
              key={`${b.s.id}-${i}`}
              title={`${name(b.s.project_id)} · ${total(b.s.seconds)}${b.s.note ? ` · ${b.s.note}` : ""}`}
              className={
                b.running
                  ? "breathe absolute inset-y-1 rounded-[3px]"
                  : "absolute inset-y-1 rounded-[3px]"
              }
              style={{
                left: `${left}%`,
                width: `${width}%`,
                // Running time is amber, everywhere in this app. A finished
                // stretch wears its project's colour.
                background: b.running ? "var(--color-punch)" : colors.get(b.s.project_id),
                opacity: b.running ? 1 : 0.85,
              }}
            />
          );
        })}
      </div>

      <div className="mt-1 flex justify-between t-caption text-faint" aria-hidden>
        {["00", "06", "12", "18", "24"].map((h) => (
          <span key={h}>{h}</span>
        ))}
      </div>
    </div>
  );
}

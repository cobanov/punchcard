import { useEffect, useState } from "react";
import { api, type AgentRun, type Project, type Session } from "../lib/api";
import { assignColors } from "../lib/palette";
import { hhmm, total } from "../lib/format";

/**
 * The day as a shape, in two bands.
 *
 * The table below says what happened and for how long; it does not say *when*,
 * or how the day was broken up. Twenty-four hours of strip with three blocks in
 * it answers a question no list answers: whether the day was one stretch or six,
 * whether the gaps are lunch or an afternoon that got away.
 *
 * The two bands are not decoration. The top one is what you declared — sessions,
 * which the database guarantees cannot overlap, so one lane is always enough.
 * The bottom is what actually ran, and agents overlap constantly: two Claude
 * sessions in two repositories at once is an ordinary Tuesday. Drawn on one line
 * those would sit end to end and lie about the day, so they are packed into
 * lanes instead, and the number of lanes is itself information — three deep
 * means three things were happening at once.
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
 * you actually navigate by. And the hours before 07:00 and after 19:00 sit on
 * darker ground, which is what turns a ruler into a day: you can see the shape
 * of when you work without reading a single number.
 */
const NIGHT = [
  [0, 7 * 60],
  [19 * 60, MINUTES],
];
const LANDMARKS = [0, 6, 12, 18, 24];

/** How many overlapping runs get their own lane before the rest are folded into
 *  the last one. Three reads as "several at once" without turning the strip
 *  into a chart nobody asked for. */
const MAX_LANES = 3;

type Span = { from: number; end: number };

/**
 * Greedy interval packing: each span goes in the first lane whose previous span
 * has already finished. Spans arrive sorted by start, which is what makes the
 * greedy choice optimal here — it is the classic interval-partitioning result,
 * not a heuristic.
 */
function packLanes<T extends Span>(spans: T[]): (T & { lane: number })[] {
  const laneEnds: number[] = [];
  return spans.map((s) => {
    let lane = laneEnds.findIndex((end) => end <= s.from);
    if (lane === -1) {
      lane = laneEnds.length < MAX_LANES ? laneEnds.length : MAX_LANES - 1;
    }
    laneEnds[lane] = Math.max(laneEnds[lane] ?? 0, s.end);
    return { ...s, lane };
  });
}

type Hover =
  | { kind: "session"; label: string; sub: string; colour: string }
  | { kind: "run"; label: string; sub: string; colour: string };

export function DayTimeline({
  day,
  sessions,
  projects,
}: {
  day: Date;
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

  const [runs, setRuns] = useState<AgentRun[]>([]);
  useEffect(() => {
    let live = true;
    const from = new Date(day);
    from.setHours(0, 0, 0, 0);
    const to = new Date(from.getTime() + 864e5);
    void api
      .agentRunsBetween(from, to)
      .then((r) => live && setRuns(r))
      .catch(() => live && setRuns([]));
    return () => {
      live = false;
    };
  }, [day]);

  const [hover, setHover] = useState<{ at: number; info: Hover } | null>(null);

  const colours = assignColors(projects);
  const name = (id: string) => projects.find((p) => p.id === id)?.name ?? "—";

  const blocks = sessions
    .map((s) => {
      const from = minuteOfDay(s.started_at);
      // A running session is drawn up to this moment; a finished one to its end.
      // Anything crossing midnight is clipped at the edge of the day rather than
      // wrapped, because the row it belongs to is on the other day's list.
      const to = s.ended_at ? minuteOfDay(s.ended_at) : now.getHours() * 60 + now.getMinutes();
      return { s, from, end: to < from ? MINUTES : to, running: !s.ended_at };
    })
    .filter((b) => b.end >= b.from);

  const runSpans = packLanes(
    runs
      .map((r) => {
        const from = minuteOfDay(r.started_at);
        const to = minuteOfDay(r.ended_at);
        return { r, from, end: to < from ? MINUTES : to };
      })
      .filter((b) => b.end >= b.from)
      .sort((a, b) => a.from - b.from),
  );
  const laneCount = Math.min(Math.max(...runSpans.map((s) => s.lane + 1), 1), MAX_LANES);

  if (!blocks.length && !runSpans.length) return null;

  return (
    <div className="border-b border-line px-3 py-2.5">
      <div className="relative overflow-hidden rounded-md bg-ink" style={{ height: 36 + (runSpans.length ? 6 + laneCount * 5 : 0) }}>
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

        {/* Declared work. */}
        {blocks.map((b, i) => (
          <span
            key={`${b.s.id}-${i}`}
            className={b.running ? "bar-rise breathe absolute rounded-[3px]" : "bar-rise absolute rounded-[3px]"}
            style={{
              left: `${pct(b.from)}%`,
              // A two-minute session is still a fact about the day. Give every
              // block a floor so it cannot round away to nothing.
              width: `${Math.max(pct(b.end - b.from), 0.35)}%`,
              top: 4,
              height: 28,
              animationDelay: `${Math.min(i * 40, 240)}ms`,
              // Running time is amber, everywhere in this app. A finished
              // stretch wears its project's colour.
              background: b.running ? "var(--color-punch)" : colours.get(b.s.project_id),
              opacity: b.running ? 1 : 0.85,
            }}
            onMouseEnter={() =>
              setHover({
                at: (b.from + b.end) / 2,
                info: {
                  kind: "session",
                  label: name(b.s.project_id),
                  sub: `${hhmm(b.s.started_at)}${b.s.ended_at ? `–${hhmm(b.s.ended_at)}` : "–now"} · ${total(b.s.seconds)}`,
                  colour: b.running ? "var(--color-punch)" : (colours.get(b.s.project_id) ?? ""),
                },
              })
            }
            onMouseLeave={() => setHover(null)}
          />
        ))}

        {/* What actually ran. Neutral rather than coloured: a run is reported,
            not verified, and colouring it like a session would claim it is the
            same kind of fact. */}
        {runSpans.map((b, i) => (
          <span
            key={`${b.r.started_at}-${i}`}
            className="bar-rise absolute rounded-[2px] bg-dim/45"
            style={{
              left: `${pct(b.from)}%`,
              width: `${Math.max(pct(b.end - b.from), 0.25)}%`,
              top: 38 + b.lane * 5,
              height: 3,
              animationDelay: `${Math.min(240 + i * 8, 400)}ms`,
            }}
            onMouseEnter={() =>
              setHover({
                at: (b.from + b.end) / 2,
                info: {
                  kind: "run",
                  label: b.r.repo || b.r.tool,
                  sub: `${hhmm(b.r.started_at)}–${hhmm(b.r.ended_at)} · ${total(b.r.seconds)}${b.r.model ? ` · ${b.r.model}` : ""}`,
                  colour: "var(--color-dim)",
                },
              })
            }
            onMouseLeave={() => setHover(null)}
          />
        ))}
      </div>

      <div className="relative mt-1 h-4">
        {LANDMARKS.map((h) => (
          <span
            key={h}
            className="absolute t-caption text-faint"
            // Positioned at the hour it names rather than spread with
            // justify-between, which puts each label near its mark instead of
            // on it. The ends pull inward so neither hangs off the strip.
            style={{
              left: `${pct(h * 60)}%`,
              transform: h === 0 ? "none" : h === 24 ? "translateX(-100%)" : "translateX(-50%)",
            }}
            aria-hidden
          >
            {String(h).padStart(2, "0")}
          </span>
        ))}

        {hover && (
          <span
            role="status"
            className="tip absolute z-10 -top-0.5 whitespace-nowrap rounded-md border border-line-strong bg-raise px-2 py-1 t-caption text-text shadow-lg"
            style={{
              left: `${Math.min(Math.max(pct(hover.at), 8), 92)}%`,
              transform: "translateX(-50%)",
            }}
          >
            <span
              className="mr-1.5 inline-block size-1.5 rounded-full align-middle"
              style={{ background: hover.info.colour }}
              aria-hidden
            />
            {hover.info.label}
            <span className="ml-1.5 tnum font-mono text-dim">{hover.info.sub}</span>
            {hover.info.kind === "run" && <span className="ml-1.5 text-faint">reported</span>}
          </span>
        )}
      </div>
    </div>
  );
}

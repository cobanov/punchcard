import { useEffect, useState } from "react";
import { api, type AgentRun, type Project, type Session } from "../lib/api";
import { assignColors } from "../lib/palette";
import { hhmm, total, workKey, workLabel } from "../lib/format";

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

/** Minutes past midnight as a clock face. */
const clock = (minutes: number) =>
  `${String(Math.floor(minutes / 60) % 24).padStart(2, "0")}:${String(Math.round(minutes) % 60).padStart(2, "0")}`;

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

/**
 * The two bands' proportions, in one place, because they encode a hierarchy.
 *
 * A declared session is the record; a reported run is evidence for it. So
 * sessions stay tall and fully saturated and runs stay shorter and dimmer — but
 * a three-pixel bar is below the height at which a rounded rectangle reads as a
 * bar at all, and at that size thirty-five of them in one neutral grey are
 * indistinguishable from each other and nearly from the background. Six pixels
 * is the floor for something meant to be seen; the hierarchy is carried by
 * opacity and by the twenty-two pixels the sessions still have on them.
 */
const SESSION_TOP = 4;
const SESSION_HEIGHT = 28;
const RUN_TOP = 38;
const RUN_HEIGHT = 6;
const RUN_GAP = 3;
const RUN_OPACITY = 0.55;

/** The narrowest a stretch may be drawn. Two pixels is a speck; four is a mark
 *  you can see and put a pointer on. */
const MIN_RUN_WIDTH = 0.45;

type Span = { from: number; end: number };

/**
 * How close two turns have to be before they are drawn as one stretch.
 *
 * The same fifteen minutes the reconstruction uses to decide a turn stopped
 * being one turn, and for the same reason: below it the pause is you reading
 * what came back or typing the next thing, above it you were gone. Using two
 * different numbers for one idea was what left a two-hour session sitting above
 * nine fragments of its own project — the gaps between them were eight and
 * twelve minutes, which is not a break.
 */
const MERGE_GAP = 15;

/** One stretch of agent work: turns that ran back to back in the same place. */
type RunStretch = {
  from: number;
  end: number;
  key: string;
  label: string;
  seconds: number;
  turns: number;
  tool: string;
  model: string;
};

/**
 * Merge adjacent turns into stretches — but only within the same repository.
 *
 * Merging everything by time alone would erase the thing the lanes exist to
 * show. Two agents in two repositories at once is the ordinary case here, and
 * collapsing them into one bar would report the day as sequential when it was
 * parallel. So each repository is its own stream, merged along its own
 * timeline, and the streams are packed into lanes afterwards.
 */
function mergeRuns(runs: AgentRun[]): RunStretch[] {
  // Directories that merely contain other work are not places work happened.
  //
  // A third of runs have no git remote, and naming their stream after the
  // directory turned ~/ and ~/Developer into projects called "cobanov" and
  // "developer" — sitting in the band next to real ones, with their own colours
  // and lanes. The test is in the data rather than in a list of names to
  // maintain: a directory that is an ancestor of another directory work
  // happened in is a parent, not a project.
  const dirs = runs.map((r) => r.cwd ?? "").filter(Boolean);
  const generic = new Set(
    dirs.filter((d) => dirs.some((other) => other !== d && other.startsWith(`${d}/`))),
  );

  const streams = new Map<string, RunStretch[]>();
  for (const r of runs) {
    const from = minuteOfDay(r.started_at);
    const to = minuteOfDay(r.ended_at);
    const end = to < from ? MINUTES : to;
    if (end < from) continue;

    // The same project arrives spelled two ways — "cobanov/herdrchat" from a
    // directory with a git remote, "herdrchat" from one without — and giving
    // them separate lanes and colours would invent a distinction nobody has.
    //
    // Work in a parent directory keeps its time but loses its name: it is
    // collected into one honest stream rather than a fictional project.
    const anonymous = !r.repo && generic.has(r.cwd ?? "");
    const key = anonymous ? "" : workKey(r.repo, r.cwd) || r.tool;
    const label = anonymous ? "elsewhere" : workLabel(r.repo, r.cwd, r.tool);

    const stream = streams.get(key) ?? [];
    const last = stream[stream.length - 1];
    if (last && from - last.end <= MERGE_GAP) {
      last.end = Math.max(last.end, end);
      last.seconds += r.seconds;
      last.turns += 1;
      if (r.model) last.model = r.model;
    } else {
      stream.push({
        from, end, key, label, seconds: r.seconds, turns: 1,
        tool: r.tool, model: r.model ?? "",
      });
    }
    streams.set(key, stream);
  }
  return [...streams.values()].flat().sort((a, b) => a.from - b.from);
}

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

  const runSpans = packLanes(mergeRuns(runs));
  // Repositories get their own colours, from the same palette the projects use.
  // Neutral grey was the honest choice when a run had to look unlike a session,
  // but it made thirty-five stretches look like one texture; the distinction is
  // carried by height and opacity instead, which say "secondary" without saying
  // "identical to each other".
  const runColours = assignColors([...new Set(runSpans.map((r) => r.key))].map((id) => ({ id })));
  const laneCount = Math.min(Math.max(...runSpans.map((s) => s.lane + 1), 1), MAX_LANES);

  if (!blocks.length && !runSpans.length) return null;

  return (
    <div className="border-b border-line px-3 py-2.5">
      <div
        className="relative overflow-hidden rounded-md bg-ink"
        style={{
          height: runSpans.length
            ? RUN_TOP + laneCount * (RUN_HEIGHT + RUN_GAP) - RUN_GAP + SESSION_TOP
            : SESSION_TOP + SESSION_HEIGHT + SESSION_TOP,
        }}
      >
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
              top: SESSION_TOP,
              height: SESSION_HEIGHT,
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
            key={`${b.key}-${b.from}-${i}`}
            className="bar-rise absolute rounded-[2px]"
            style={{
              left: `${pct(b.from)}%`,
              width: `${Math.max(pct(b.end - b.from), MIN_RUN_WIDTH)}%`,
              top: RUN_TOP + b.lane * (RUN_HEIGHT + RUN_GAP),
              height: RUN_HEIGHT,
              background: runColours.get(b.key),
              opacity: RUN_OPACITY,
              animationDelay: `${Math.min(240 + i * 8, 400)}ms`,
            }}
            onMouseEnter={() =>
              setHover({
                at: (b.from + b.end) / 2,
                info: {
                  kind: "run",
                  label: b.label,
                  // Both numbers, because they answer different questions: how
                  // long the stretch lasted, and how much agent time went into
                  // it. Parallel turns make the second larger than the first,
                  // and hiding that would be the misleading choice.
                  sub: `${clock(b.from)}–${clock(b.end)} · ${total(b.seconds)} over ${b.turns} turn${b.turns === 1 ? "" : "s"}`,
                  colour: runColours.get(b.key) ?? "var(--color-dim)",
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

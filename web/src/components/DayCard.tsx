import { useMemo } from "react";
import type { Cluster, Commit, Project, Session } from "../lib/api";
import { elapsed, firstLine, hhmm, hourOf, total } from "../lib/format";

/**
 * The day, drawn as a punch card.
 *
 * Every other time tracker draws the day as a list. A list answers "what did I
 * do" and stops there. This draws time as a vertical scale with sessions as
 * bands and commits as punches inside them — which answers a second question
 * the product exists for: which stretches of the day have evidence, and which
 * do not.
 *
 * That second question is what makes the unmatched commits legible. A commit
 * with no session around it appears as a hollow punch sitting in empty space,
 * and "work with no record" stops being a section heading and becomes something
 * you see. The name of the product is not decoration either: this is the object
 * it is named after.
 *
 * The list did not go away — every band carries its label and duration on the
 * right. It is a timeline and a list at once, not a chart nobody can read.
 */

/** The vertical scale. One hour is --hour tall; everything is derived from it,
 *  so a two-hour session is exactly twice the height of a one-hour one. */
const HOUR = "var(--hour)";
const pos = (hours: number) => `calc(${HOUR} * ${hours})`;

interface Props {
  sessions: Session[];
  commits: Record<string, Commit[]>;
  clusters: Cluster[];
  projects: Project[];
  onStop: () => void;
  onRecover: (cluster: Cluster) => void;
  now: number;
}

export function DayCard({ sessions, commits, clusters, projects, onStop, onRecover, now }: Props) {
  const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? "—";

  // Only draw the hours the day actually used, plus a little air. A card that
  // always starts at midnight is mostly empty space for anyone who does not
  // work at midnight.
  const { first, last } = useMemo(() => {
    const marks: number[] = [];
    for (const s of sessions) {
      marks.push(hourOf(s.started_at));
      marks.push(s.ended_at ? hourOf(s.ended_at) : hourOf(new Date(now)));
    }
    for (const c of clusters) {
      marks.push(hourOf(c.from));
      marks.push(hourOf(c.to));
    }
    if (!marks.length) return { first: 9, last: 18 };
    return {
      first: Math.max(0, Math.floor(Math.min(...marks)) - 1),
      last: Math.min(24, Math.ceil(Math.max(...marks)) + 1),
    };
  }, [sessions, clusters, now]);

  const hours = Array.from({ length: last - first }, (_, i) => first + i);

  if (!sessions.length && !clusters.length) {
    return (
      <div className="py-16 text-center text-dim">
        <p>Nothing recorded today.</p>
        <p className="mt-1 text-faint">Pick a project above and press start.</p>
      </div>
    );
  }

  return (
    <div className="relative" style={{ height: pos(last - first) }}>
      {/* The hour rail. Hairlines rather than a grid: the scale should be
          readable without becoming the thing you look at. */}
      {hours.map((h) => (
        <div
          key={h}
          className="absolute inset-x-0 flex items-start gap-3"
          style={{ top: pos(h - first) }}
        >
          <span className="w-8 shrink-0 text-right font-mono text-[11px] leading-none text-faint tabular-nums">
            {String(h).padStart(2, "0")}
          </span>
          <div className="mt-[0.3em] h-px flex-1 bg-line" />
        </div>
      ))}

      {/* Sessions. */}
      {sessions.map((session) => {
        const from = hourOf(session.started_at);
        const to = session.ended_at ? hourOf(session.ended_at) : hourOf(new Date(now));
        const height = Math.max(to - from, 0.09); // a very short session must still be visible
        const running = session.running;
        const own = commits[session.id] ?? [];

        return (
          <div
            key={session.id}
            className="absolute left-11 right-0 flex gap-3"
            style={{ top: pos(from - first), height: pos(height) }}
          >
            <div
              className={[
                "relative w-1.5 shrink-0 rounded-full transition-all",
                running ? "bg-punch" : "bg-ghost",
              ].join(" ")}
              aria-hidden
            />
            <div className="min-w-0 flex-1 py-0.5">
              <div className="flex items-baseline gap-2">
                <span className="truncate font-medium">{projectName(session.project_id)}</span>
                <span className="min-w-0 flex-1 truncate text-dim">{session.note || "—"}</span>
                <span className="shrink-0 font-mono text-[12px] tabular-nums text-dim">
                  {running ? (
                    <button
                      onClick={onStop}
                      className="rounded bg-punch px-2 py-0.5 font-medium text-ink hover:brightness-110"
                    >
                      Stop {total(elapsed(session))}
                    </button>
                  ) : (
                    total(session.seconds)
                  )}
                </span>
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-1.5">
                {own.map((commit) => (
                  <Punch key={commit.sha} commit={commit} filled />
                ))}
                {session.commit_sync_state === "pending" && (
                  <span className="font-mono text-[11px] text-faint">looking for commits…</span>
                )}
              </div>
            </div>
          </div>
        );
      })}

      {/* Unmatched commits: punches with no card around them.
          This is the product's second half, and drawing it this way is the
          reason the card exists at all. */}
      {clusters.map((cluster) => {
        const from = hourOf(cluster.from);
        const to = hourOf(cluster.to);
        return (
          <div
            key={cluster.from}
            className="absolute left-11 right-0 flex gap-3"
            style={{ top: pos(from - first), height: pos(Math.max(to - from, 0.09)) }}
          >
            <div
              className="w-1.5 shrink-0 rounded-full border border-dashed border-ghost"
              aria-hidden
            />
            <div className="min-w-0 flex-1 py-0.5">
              <div className="flex items-baseline gap-2">
                <span className="truncate text-dim">
                  {cluster.repos.join(", ")}
                </span>
                <span className="min-w-0 flex-1 truncate text-faint">
                  {hhmm(cluster.from)}–{hhmm(cluster.to)} · no timer was running
                </span>
                <button
                  onClick={() => onRecover(cluster)}
                  className="shrink-0 rounded border border-line px-2 py-0.5 text-[12px] text-dim hover:border-punch hover:text-punch"
                >
                  Record this
                </button>
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-1.5">
                {cluster.commits.map((commit) => (
                  <Punch key={commit.sha} commit={commit} filled={false} />
                ))}
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

/** One commit.
 *
 *  Filled when a session covers it, hollow when nothing does. That single
 *  difference is the whole vocabulary of the card: solid means accounted for. */
function Punch({ commit, filled }: { commit: Commit; filled: boolean }) {
  const title = `${commit.sha.slice(0, 7)} · ${hhmm(commit.committed_at)}\n${firstLine(commit.message)}`;
  const shared =
    "punch-in inline-flex h-3.5 w-3.5 items-center justify-center rounded-full transition-transform hover:scale-125";
  return (
    <a
      href={commit.url}
      target="_blank"
      rel="noreferrer"
      title={title}
      aria-label={title}
      className={
        filled
          ? `${shared} bg-punch`
          : `${shared} border border-punch-dim bg-transparent hover:border-punch`
      }
    />
  );
}

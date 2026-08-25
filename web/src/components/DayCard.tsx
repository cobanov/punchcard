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
  onRecover: (cluster: Cluster) => void;
  now: number;
}

export function DayCard({ sessions, commits, clusters, projects, onRecover, now }: Props) {
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
    // An empty day is an invitation, not a void — and it is also the only place
    // the card's vocabulary can be taught before there is anything on it.
    return (
      <div className="flex items-center gap-6 border border-dashed border-line px-5 py-8">
        <div className="flex flex-col gap-1.5" aria-hidden>
          <span className="inline-block h-2.5 w-2.5 rounded-full bg-punch" />
          <span className="inline-block h-2.5 w-2.5 rounded-full border border-punch-deep" />
        </div>
        <div>
          <p className="text-dim">Nothing recorded today.</p>
          <p className="mt-1 text-faint">
            Filled marks are commits inside a session. Hollow ones happened with no timer
            running — press start above and the day fills in.
          </p>
        </div>
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
          <span className="tnum w-6 shrink-0 text-right font-mono text-[10px] leading-none text-faint">
            {String(h).padStart(2, "0")}
          </span>
          <div className="mt-[0.35em] h-px flex-1 bg-line" />
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
            className="absolute left-8 right-0 flex gap-2.5"
            style={{ top: pos(from - first), height: pos(height) }}
          >
            <div
              className={[
                "w-[3px] shrink-0 rounded-sm",
                running ? "bg-punch" : "bg-ghost",
              ].join(" ")}
              aria-hidden
            />
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline gap-2 leading-tight">
                <span className="truncate font-medium">{projectName(session.project_id)}</span>
                <span className="min-w-0 flex-1 truncate text-dim">{session.note || "—"}</span>
                {/* No Stop button here: the bar above already has one, and a
                    second control for the same action makes the reader work out
                    whether they differ. */}
                <span
                  className={[
                    "tnum shrink-0 font-mono text-[11px]",
                    running ? "text-punch" : "text-dim",
                  ].join(" ")}
                >
                  {total(running ? elapsed(session) : session.seconds)}
                </span>
              </div>
              <div className="mt-0.5 flex flex-wrap items-center gap-1">
                {own.map((commit) => (
                  <Punch key={commit.sha} commit={commit} filled />
                ))}
                {session.commit_sync_state === "pending" && (
                  <span className="font-mono text-[10px] text-faint">looking for commits…</span>
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
            className="absolute left-8 right-0 flex gap-2.5"
            style={{ top: pos(from - first), height: pos(Math.max(to - from, 0.09)) }}
          >
            <div
              className="w-[3px] shrink-0 rounded-sm border border-dashed border-ghost"
              aria-hidden
            />
            {/* The punches lead. They are the point: work that happened with
                nothing recording it. The words underneath explain them, rather
                than the other way round. */}
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <div className="flex flex-wrap items-center gap-1">
                  {cluster.commits.map((commit) => (
                    <Punch key={commit.sha} commit={commit} filled={false} />
                  ))}
                </div>
                <button
                  onClick={() => onRecover(cluster)}
                  className="btn-quiet shrink-0 px-1.5 py-0 text-[11px] hover:border-punch hover:text-punch"
                >
                  Record this
                </button>
              </div>
              <p className="mt-0.5 truncate text-[11px] text-faint">
                {cluster.commits.length} commit{cluster.commits.length === 1 ? "" : "s"} ·{" "}
                {hhmm(cluster.from)}–{hhmm(cluster.to)} · no timer was running ·{" "}
                <span className="font-mono">{summarise(cluster.repos)}</span>
              </p>
            </div>
          </div>
        );
      })}
    </div>
  );
}

/** Repositories, said the way a person would: two by name, the rest counted. */
function summarise(repos: string[]): string {
  if (repos.length <= 2) return repos.join(", ");
  return `${repos.slice(0, 2).join(", ")} +${repos.length - 2}`;
}

/** One commit.
 *
 *  Filled when a session covers it, hollow when nothing does. That single
 *  difference is the whole vocabulary of the card: solid means accounted for. */
function Punch({ commit, filled }: { commit: Commit; filled: boolean }) {
  const title = `${commit.sha.slice(0, 7)} · ${hhmm(commit.committed_at)}\n${firstLine(commit.message)}`;
  const shared =
    "punch-in inline-block h-2.5 w-2.5 rounded-full transition-transform duration-150 hover:scale-150";
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
          : `${shared} border border-punch-deep bg-transparent hover:border-punch`
      }
    />
  );
}

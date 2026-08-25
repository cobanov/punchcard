import { useState } from "react";
import type { Cluster, Commit, Project, Session } from "../lib/api";
import { firstLine, hhmm, total } from "../lib/format";

/**
 * The day, as rows.
 *
 * This replaces the drawn timeline. The timeline was the design's one big
 * swing, and it lost on the terms that matter for this product: rows collided
 * when sessions were close together, short sessions were unreadable, and a
 * normal two-session day looked like an empty chart. A tool you open for ten
 * seconds cannot ask to be interpreted.
 *
 * What survives from it is the vocabulary, not the geometry: a session with
 * commits carries amber evidence, and work no timer covered still appears —
 * as a quiet dashed card offering to become a record, not as a diagram.
 *
 * Rows are newest first, because the question a glance answers is "what was I
 * just doing". Clicking a row opens its commits; everything else about a row is
 * legible without touching it.
 */

interface Props {
  sessions: Session[];
  commits: Record<string, Commit[]>;
  projects: Project[];
}

export function SessionList({ sessions, commits, projects }: Props) {
  const [open, setOpen] = useState<string | null>(null);
  const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? "—";

  // The running session is the timer bar's job. Showing it here too would put
  // the same fact on screen twice and make the reader check whether they agree.
  const finished = sessions.filter((s) => !s.running);

  if (!finished.length) {
    return (
      <p className="px-3 py-6 text-center text-faint">
        Nothing recorded yet today. Start a timer above — sessions land here when
        you stop them.
      </p>
    );
  }

  return (
    <ul className="divide-y divide-line">
      {finished.map((session) => {
        const own = commits[session.id] ?? [];
        const expanded = open === session.id;
        return (
          <li key={session.id}>
            <button
              onClick={() => setOpen(expanded ? null : session.id)}
              aria-expanded={expanded}
              className="flex w-full items-baseline gap-3 px-3 py-2 text-left transition-colors duration-100 hover:bg-raise/60"
            >
              <span className="tnum w-[5.5rem] shrink-0 font-mono text-[11px] text-faint">
                {hhmm(session.started_at)}–{session.ended_at ? hhmm(session.ended_at) : "…"}
              </span>
              <span className="w-32 shrink-0 truncate font-medium">
                {projectName(session.project_id)}
              </span>
              <span className="min-w-0 flex-1 truncate text-dim">{session.note || "—"}</span>
              <CommitBadge count={own.length} syncState={session.commit_sync_state} />
              <span className="tnum w-14 shrink-0 text-right font-mono text-[11px] text-dim">
                {total(session.seconds)}
              </span>
            </button>

            {expanded && (
              <div className="border-t border-line/60 bg-ink/40 px-3 py-2 pl-[6.25rem]">
                {own.length === 0 ? (
                  <p className="text-[12px] text-faint">
                    {session.commit_sync_state === "pending"
                      ? "Still looking for commits."
                      : "No commits were pushed during this session."}
                  </p>
                ) : (
                  <ul className="space-y-1">
                    {own.map((commit) => (
                      <li key={commit.sha} className="flex items-baseline gap-2.5 text-[12px]">
                        <a
                          href={commit.url}
                          target="_blank"
                          rel="noreferrer"
                          className="shrink-0 font-mono text-punch hover:underline"
                        >
                          {commit.sha.slice(0, 7)}
                        </a>
                        <span className="min-w-0 flex-1 truncate text-dim">
                          {firstLine(commit.message)}
                        </span>
                        <span className="shrink-0 font-mono text-[11px] text-faint">
                          {commit.repo}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

/** The evidence, at a glance. Amber only when there is any — a zero would put
 *  the accent on every row and drain it of meaning. */
function CommitBadge({ count, syncState }: { count: number; syncState: string }) {
  if (count > 0) {
    return (
      <span className="tnum shrink-0 rounded-full border border-punch-deep px-1.5 font-mono text-[10px] leading-[1.6] text-punch">
        {count}
      </span>
    );
  }
  if (syncState === "pending") {
    return <span className="shrink-0 text-[10px] text-faint">sync…</span>;
  }
  return <span className="w-4 shrink-0" aria-hidden />;
}

/**
 * Work that happened while no timer was running.
 *
 * One card per stretch, one press to record it. The project is pre-picked when
 * the guess is safe (one repository, one project) and left to the select when
 * it is not — recording under the wrong client is worse than one extra click.
 */
export function UnmatchedList({
  clusters,
  projects,
  onRecover,
  busy,
}: {
  clusters: Cluster[];
  projects: Project[];
  onRecover: (cluster: Cluster, projectID: string) => void;
  busy: boolean;
}) {
  if (!clusters.length) return null;
  return (
    <div className="mt-3 space-y-2">
      {clusters.map((cluster, i) => (
        <UnmatchedRow
          key={`${cluster.from}-${i}`}
          cluster={cluster}
          projects={projects}
          onRecover={onRecover}
          busy={busy}
        />
      ))}
    </div>
  );
}

function UnmatchedRow({
  cluster,
  projects,
  onRecover,
  busy,
}: {
  cluster: Cluster;
  projects: Project[];
  onRecover: (cluster: Cluster, projectID: string) => void;
  busy: boolean;
}) {
  const [projectID, setProjectID] = useState(
    cluster.suggested_project_id ?? projects[0]?.id ?? "",
  );
  const n = cluster.commits.length;
  const repos =
    cluster.repos.length <= 2
      ? cluster.repos.join(", ")
      : `${cluster.repos.slice(0, 2).join(", ")} +${cluster.repos.length - 2}`;

  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-dashed border-line px-3 py-2">
      <span
        className="h-2 w-2 shrink-0 rounded-full border border-punch"
        aria-hidden
        title="Commits with no session around them"
      />
      <span className="min-w-0 flex-1 truncate text-[12px] text-dim">
        <span className="text-text">{n} commit{n === 1 ? "" : "s"}</span>
        {" · "}
        {dayLabel(cluster.from)}
        {hhmm(cluster.from)}–{hhmm(cluster.to)} · no timer was running ·{" "}
        <span className="font-mono text-[11px]">{repos}</span>
      </span>
      <select
        value={projectID}
        onChange={(e) => setProjectID(e.target.value)}
        aria-label="Project to record this under"
        className="field max-w-[9rem] py-0.5 text-[12px]"
      >
        {projects.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </select>
      <button
        onClick={() => projectID && onRecover(cluster, projectID)}
        disabled={busy || !projectID}
        className="btn-ghost py-0.5 text-[12px] hover:border-punch hover:text-punch"
      >
        Record
      </button>
    </div>
  );
}

/** "12 Aug " when the stretch is from another day, nothing when it is today's.
 *  Recovery covers a week, and Friday's forgotten timer needs its date. */
function dayLabel(iso: string): string {
  const d = new Date(iso);
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  if (d >= today) return "";
  return d.toLocaleDateString([], { day: "numeric", month: "short" }) + " ";
}

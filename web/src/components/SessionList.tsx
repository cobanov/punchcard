import { useState } from "react";
import type { Cluster, Commit, Project, Session } from "../lib/api";
import { firstLine, hhmm, total } from "../lib/format";

/**
 * The day, as rows.
 *
 * This replaced the drawn timeline: rows collided, short sessions were
 * unreadable, and a tool you open for ten seconds cannot ask to be interpreted.
 * What survives is the vocabulary — amber means evidence — not the geometry.
 *
 * Rows are newest first, because the glance answers "what was I just doing".
 * A row expands on click into everything behind it: its commits, and the
 * controls to correct it. Editing lives inside the row rather than in a modal,
 * because a correction is a small thing and the UI should treat it as one.
 */

interface Props {
  sessions: Session[];
  commits: Record<string, Commit[]>;
  projects: Project[];
  onSave: (id: string, body: Record<string, unknown>) => void;
  onDelete: (id: string) => void;
  busy: boolean;
}

export function SessionList({ sessions, commits, projects, onSave, onDelete, busy }: Props) {
  const [open, setOpen] = useState<string | null>(null);
  const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? "—";

  // The running session is the timer bar's job; here it would be the same fact
  // twice. Browsed days never contain it, so this only bites on today.
  const finished = sessions.filter((s) => !s.running);

  if (!finished.length) {
    return (
      <p className="px-3 py-6 text-center text-faint">
        Nothing recorded this day.
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
              <div className="space-y-2 border-t border-line/60 bg-ink/40 px-3 py-2.5">
                <EditRow
                  session={session}
                  projects={projects}
                  onSave={(body) => onSave(session.id, body)}
                  onDelete={() => onDelete(session.id)}
                  busy={busy}
                />
                {own.length > 0 && (
                  <ul className="space-y-1 pt-1">
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

/**
 * The correction controls: project, note, start, end, save — and delete, held
 * apart on the right so it cannot be hit reaching for save.
 *
 * Times are edited as clock times on the session's own day; the date part is
 * taken from the record, so correcting yesterday's entry from today cannot drag
 * it to today. The server re-scans commits by itself when the window moves.
 */
function EditRow({
  session,
  projects,
  onSave,
  onDelete,
  busy,
}: {
  session: Session;
  projects: Project[];
  onSave: (body: Record<string, unknown>) => void;
  onDelete: () => void;
  busy: boolean;
}) {
  const [projectID, setProjectID] = useState(session.project_id);
  const [note, setNote] = useState(session.note);
  const [start, setStart] = useState(timeInput(session.started_at));
  const [end, setEnd] = useState(session.ended_at ? timeInput(session.ended_at) : "");

  const dirty =
    projectID !== session.project_id ||
    note !== session.note ||
    start !== timeInput(session.started_at) ||
    (session.ended_at !== null && end !== timeInput(session.ended_at));

  const save = () => {
    const body: Record<string, unknown> = {};
    if (projectID !== session.project_id) body.project_id = projectID;
    if (note !== session.note) body.note = note;
    if (start !== timeInput(session.started_at)) {
      body.started_at = onDay(session.started_at, start);
    }
    if (session.ended_at && end !== timeInput(session.ended_at)) {
      body.ended_at = onDay(session.ended_at, end);
    }
    if (Object.keys(body).length) onSave(body);
  };

  return (
    <div className="flex flex-wrap items-center gap-2">
      <select
        value={projectID}
        onChange={(e) => setProjectID(e.target.value)}
        aria-label="Project"
        className="field max-w-[9rem] py-0.5 text-[12px]"
      >
        {projects.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </select>
      <input
        value={note}
        onChange={(e) => setNote(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && save()}
        placeholder="What was this?"
        aria-label="Note"
        className="field min-w-32 flex-1 py-0.5 text-[12px]"
      />
      <input
        type="time"
        value={start}
        onChange={(e) => setStart(e.target.value)}
        aria-label="Start time"
        className="field py-0.5 font-mono text-[12px]"
      />
      <span className="text-faint">–</span>
      <input
        type="time"
        value={end}
        onChange={(e) => setEnd(e.target.value)}
        aria-label="End time"
        disabled={!session.ended_at}
        className="field py-0.5 font-mono text-[12px]"
      />
      <button onClick={save} disabled={busy || !dirty} className="btn-ghost py-0.5 text-[12px]">
        Save
      </button>
      <button
        onClick={() => confirm("Delete this session? Its commits become unmatched again.") && onDelete()}
        disabled={busy}
        className="btn-bare ml-auto text-[12px] hover:text-punch"
      >
        Delete
      </button>
    </div>
  );
}

/** ISO timestamp → the HH:MM a time input speaks, in local time. */
function timeInput(iso: string): string {
  const d = new Date(iso);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

/** A clock time re-applied to the timestamp's own calendar day. */
function onDay(iso: string, hhmmValue: string): string {
  const d = new Date(iso);
  const [h, m] = hhmmValue.split(":").map(Number);
  d.setHours(h ?? 0, m ?? 0, 0, 0);
  return d.toISOString();
}

/** The evidence, at a glance. Amber only when there is any — a zero on every
 *  row would drain the accent of meaning. Commits are optional garnish here,
 *  not structure: a session without them is a complete record. */
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
 * Work that happened while no timer was running. One card per stretch, one
 * press to record it; the project select is on the card because recording under
 * the wrong client is worse than one extra click.
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
        {clusterDayLabel(cluster.from)}
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

/** "12 Aug " when the stretch is from another day; nothing when today's. */
function clusterDayLabel(iso: string): string {
  const d = new Date(iso);
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  if (d >= today) return "";
  return d.toLocaleDateString([], { day: "numeric", month: "short" }) + " ";
}

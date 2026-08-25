import { useEffect, useState, type CSSProperties } from "react";
import { api, type AgentRun, type Cluster, type Commit, type Project, type Session } from "../lib/api";
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
    // One template for the header and every row below it. The commit cell is a
    // fixed track rather than a badge that sizes itself, so the note beside it
    // truncates at the same pixel on every row.
    <div style={{ "--tbl-cols": "5.5rem 9rem minmax(0, 1fr) 3.5rem 4.5rem" } as CSSProperties}>
      <div className="tbl-head">
        <span>Time</span>
        <span>Project</span>
        <span>Note</span>
        <span className="text-center">Commits</span>
        <span className="text-right">Duration</span>
      </div>
      <ul className="divide-y divide-line">
      {finished.map((session) => {
        const own = commits[session.id] ?? [];
        const expanded = open === session.id;
        return (
          <li key={session.id}>
            <button
              onClick={() => setOpen(expanded ? null : session.id)}
              aria-expanded={expanded}
              className="tbl-row w-full transition-colors duration-100 hover:bg-raise/60"
            >
              <span className="tnum t-caption font-mono text-faint">
                {hhmm(session.started_at)}–{session.ended_at ? hhmm(session.ended_at) : "…"}
              </span>
              <span className="truncate font-medium">
                {projectName(session.project_id)}
              </span>
              <span className="truncate text-dim">{session.note || "—"}</span>
              <CommitBadge count={own.length} syncState={session.commit_sync_state} />
              <span className="tbl-num t-caption text-dim">
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
                      <li key={commit.sha} className="flex items-baseline gap-2.5 t-body">
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
                        <span className="shrink-0 font-mono t-caption text-faint">
                          {commit.repo}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
                <AgentRunList sessionID={session.id} />
              </div>
            )}
          </li>
        );
      })}
      </ul>
    </div>
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
        className="select max-w-[9rem] py-0.5 t-caption"
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
        className="field min-w-32 flex-1 py-0.5 t-body"
      />
      <input
        type="time"
        value={start}
        onChange={(e) => setStart(e.target.value)}
        aria-label="Start time"
        className="field py-0.5 font-mono t-body"
      />
      <span className="text-faint">–</span>
      <input
        type="time"
        value={end}
        onChange={(e) => setEnd(e.target.value)}
        aria-label="End time"
        disabled={!session.ended_at}
        className="field py-0.5 font-mono t-body"
      />
      <button onClick={save} disabled={busy || !dirty} className="btn-ghost py-0.5 t-body">
        Save
      </button>
      <button
        onClick={() => confirm("Delete this session? Its commits become unmatched again.") && onDelete()}
        disabled={busy}
        className="btn-bare ml-auto t-body hover:text-punch"
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
/**
 * The commit count, in a cell that is the same width whether or not there is
 * one. The badge used to size itself, which made every note beside it truncate
 * somewhere slightly different — the column edge came out ragged with no row
 * being individually wrong. The track is fixed now and the badge centres in it.
 */
function CommitBadge({ count, syncState }: { count: number; syncState: string }) {
  return (
    <span className="flex justify-center">
      {count > 0 ? (
        <span
          title={`${count} commit${count === 1 ? "" : "s"} in this session`}
          className="tnum rounded-full border border-punch-deep px-1.5 font-mono t-caption text-punch"
        >
          {count}
        </span>
      ) : syncState === "pending" ? (
        <span className="t-caption text-faint" title="Looking for commits">
          ·
        </span>
      ) : null}
    </span>
  );
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
  onRecover: (cluster: Cluster, target: RecoverTarget) => void;
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

/**
 * Where a recovered stretch should be filed.
 *
 * Either an existing project, or one that does not exist yet — because the
 * commits themselves name it. A stretch of work in `cobanov/herdzchat` with no
 * project to put it under used to be a dead end: leave the app, create a
 * project by hand, come back, and hope the suggestion was still there.
 */
export type RecoverTarget = { projectID: string } | { newProject: string; repo: string };

/** "cobanov/herdzchat" is a project called herdzchat. The owner is the same for
 *  every repository a person owns, so it carries no information here. */
function repoBase(full: string): string {
  const slash = full.lastIndexOf("/");
  return slash === -1 ? full : full.slice(slash + 1);
}

const CREATE = "__create__";

function UnmatchedRow({
  cluster,
  projects,
  onRecover,
  busy,
}: {
  cluster: Cluster;
  projects: Project[];
  onRecover: (cluster: Cluster, target: RecoverTarget) => void;
  busy: boolean;
}) {
  const repo = cluster.repos[0] ?? "";
  // A repository names the project best. Failing that, a run's working
  // directory is the next honest guess — it is what the person called the
  // folder they were working in.
  const suggestedName = repo ? repoBase(repo) : (cluster.dirs?.[0] ?? "");
  const runs = cluster.agent_runs ?? [];
  const agentSeconds = runs.reduce((sum, r) => sum + r.seconds, 0);
  // A project already named after the repository is almost certainly the one
  // meant, even when nothing was ever linked.
  const byName = projects.find(
    (p) => suggestedName && p.name.toLowerCase() === suggestedName.toLowerCase(),
  );

  // Preference, strongest first: what the server worked out from a linked
  // repository, then a project that simply shares the repository's name, then
  // creating that project, and only then whatever happens to be first.
  const [choice, setChoice] = useState(
    cluster.suggested_project_id ??
      byName?.id ??
      (suggestedName ? CREATE : projects[0]?.id ?? ""),
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
      <span className="min-w-0 flex-1 truncate t-body text-dim">
        <span className="text-text">
          {n > 0 && `${n} commit${n === 1 ? "" : "s"}`}
          {n > 0 && runs.length > 0 && ", "}
          {/* A run knows how long it lasted, so say so — that is the number
              being offered as a record, and it is more use than a count. */}
          {runs.length > 0 && `${total(agentSeconds)} of agent work`}
        </span>
        {" · "}
        {clusterDayLabel(cluster.from)}
        {hhmm(cluster.from)}–{hhmm(cluster.to)} · no timer was running ·{" "}
        <span className="font-mono t-caption">{repos || cluster.dirs?.join(", ") || "—"}</span>
      </span>
      <select
        value={choice}
        onChange={(e) => setChoice(e.target.value)}
        aria-label="Project to record this under"
        className="select max-w-[11rem] py-0.5 t-caption"
      >
        {suggestedName && <option value={CREATE}>+ new “{suggestedName}”</option>}
        {projects.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </select>
      <button
        onClick={() =>
          choice &&
          onRecover(
            cluster,
            choice === CREATE ? { newProject: suggestedName, repo } : { projectID: choice },
          )
        }
        disabled={busy || !choice}
        className="btn-ghost py-0.5 t-body hover:border-punch hover:text-punch"
      >
        {choice === CREATE ? "Create & record" : "Record"}
      </button>
    </div>
  );
}

/**
 * The agent runs filed against a session.
 *
 * Fetched when the row opens rather than with the day: this is the only place
 * they appear, and loading them for every session on every day view would
 * double the requests to show something nobody had asked to see yet.
 *
 * They render below the commits and quieter than them, on purpose. A commit is
 * something punchcard fetched from GitHub and can prove; a run is what a local
 * hook said happened. Showing them at the same weight would claim a certainty
 * the second kind does not have — hence the word "reported", said once.
 */
function AgentRunList({ sessionID }: { sessionID: string }) {
  const [runs, setRuns] = useState<AgentRun[] | null>(null);

  useEffect(() => {
    let live = true;
    void api
      .agentRuns(sessionID)
      .then((r) => live && setRuns(r))
      .catch(() => live && setRuns([]));
    return () => {
      live = false;
    };
  }, [sessionID]);

  if (!runs?.length) return null;

  return (
    <div className="pt-1.5">
      <p className="eyebrow mb-1">Reported by agents</p>
      <ul className="space-y-1">
        {runs.map((run, i) => (
          <li key={`${run.tool}-${run.started_at}-${i}`} className="flex items-baseline gap-2.5 t-caption text-faint">
            <span className="shrink-0 font-mono text-dim">{run.tool}</span>
            <span className="shrink-0 tnum font-mono">
              {hhmm(run.started_at)}–{hhmm(run.ended_at)}
            </span>
            <span className="shrink-0 tnum">{total(run.seconds)}</span>
            {run.model && <span className="min-w-0 truncate font-mono">{run.model}</span>}
            {run.tool_calls != null && (
              <span className="ml-auto shrink-0 tnum">
                {run.tool_calls} tool{run.tool_calls === 1 ? "" : "s"}
              </span>
            )}
          </li>
        ))}
      </ul>
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

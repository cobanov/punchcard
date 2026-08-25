import { useState } from "react";
import {
  api,
  type Cluster,
  type Commit,
  type Project,
  type Session,
  type SessionAllocation,
  type SessionAttributionT,
  type UnresolvedPlace,
} from "../lib/api";
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
  attribution: Record<string, SessionAttributionT>;
  projects: Project[];
  onSave: (id: string, body: Record<string, unknown>) => void;
  onDelete: (id: string) => void;
  /** Reload the day — a project created from an unresolved place changes what
   *  every other session on screen resolves to. */
  onChanged: () => void;
  busy: boolean;
}

export function SessionList({ sessions, commits, attribution, projects, onSave, onDelete, onChanged, busy }: Props) {
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
    // One template for the header and every row below it, declared in CSS so a
    // media query can rearrange it. The commit cell is a fixed track rather
    // than a badge that sizes itself, so the note beside it truncates at the
    // same pixel on every row.
    <div className="tbl-sessions">
      <div className="tbl-head hidden mid:grid">
        <span className="c-time">Time</span>
        <span className="c-project">Project</span>
        <span className="c-note">Note</span>
        <span className="c-commits text-center">Commits</span>
        <span className="c-dur text-right">Duration</span>
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
              <span className="c-time tnum t-caption font-mono text-faint">
                {hhmm(session.started_at)}–{session.ended_at ? hhmm(session.ended_at) : "…"}
              </span>
              <span className="c-project truncate font-medium">
                {projectName(session.project_id)}
                {drifts(attribution[session.id], session.project_id) && (
                  <span
                    className="ml-1.5 t-caption text-faint"
                    title="Evidence points to other projects — open the row"
                  >
                    •
                  </span>
                )}
              </span>
              <span className="c-note truncate text-dim">{session.note || "—"}</span>
              <CommitBadge count={own.length} syncState={session.commit_sync_state} />
              <span className="c-dur tbl-num t-caption text-dim">
                {total(session.seconds)}
              </span>
            </button>

            {expanded && (
              <div className="row-reveal space-y-2 border-t border-line/60 bg-ink/40 px-3 py-2.5">
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
                <AttributionBlock
                  session={session}
                  attribution={attribution[session.id]}
                  onChanged={onChanged}
                />
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
    <span className="c-commits flex justify-center mid:justify-center">
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
  // Repos and dirs are separate fields on the wire, but a directory that is
  // simply the repo's own folder is not a second place — show one.
  const places = [
    ...(cluster.repos ?? []).map((r) => repoBase(r)),
    ...(cluster.dirs ?? []),
  ].filter((v, i, all) => all.indexOf(v) === i);
  const runs = cluster.agent_runs ?? [];
  const agentSeconds = runs.reduce((sum, r) => sum + r.seconds, 0);
  // Wall time, which is what recording this actually produces. Agents run in
  // parallel, so their hours add up to more than the stretch they ran in — the
  // first real cluster on screen said "2h 34m" above a window of 74 minutes,
  // which is two true numbers arranged into a false impression.
  const spanSeconds = Math.max(
    0,
    Math.round((new Date(cluster.to).getTime() - new Date(cluster.from).getTime()) / 1000),
  );
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
              being offered as a record, and it is more use than a count. Under
              a minute there is no duration worth showing: "0m of agent work"
              reads as a bug rather than as a very short turn. */}
          {runs.length > 0 &&
            (agentSeconds >= 60
              ? `${total(spanSeconds)} with agents`
              : `${runs.length} agent run${runs.length === 1 ? "" : "s"}`)}
        </span>
        {" · "}
        {clusterDayLabel(cluster.from)}
        {hhmm(cluster.from)}–{hhmm(cluster.to)}
        {runs.length > 0 && agentSeconds > spanSeconds + 60 && (
          <span title="Agents ran in parallel, so their time adds up to more than the stretch">
            {" "}· {total(agentSeconds)} across {runs.length} runs
          </span>
        )}{" "}
        · no timer was running ·{" "}
        <span className="font-mono t-caption">
          {places.length <= 2 ? places.join(", ") : `${places.slice(0, 2).join(", ")} +${places.length - 2}`}
        </span>
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
 * How this session's hour actually divides, and the places nobody claims.
 *
 * This replaces a per-repository summary of agent runs: the same evidence,
 * upgraded from grouping strings to real resolution, and now able to say WHY
 * each project earned its minutes. Quiet and dim on purpose — a commit is
 * proof, a derived minute is a reading of proof — and never amber, which keeps
 * meaning running time and commit evidence.
 */
function AttributionBlock({
  session,
  attribution,
  onChanged,
}: {
  session: Session;
  attribution?: SessionAttributionT;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  if (!attribution) return null;
  const { allocations, unresolved } = attribution;
  // A session whose evidence agrees with its declaration has nothing to add
  // that the row above does not already say.
  if (!allocations.some((a) => a.evidenced) && unresolved.length === 0) return null;

  const reasonLabel = (a: SessionAllocation) =>
    a.reason === "linked" ? "linked" : a.reason === "name" ? "name match" : "declared, quiet";

  const createFrom = async (u: UnresolvedPlace) => {
    setBusy(true);
    try {
      const p = await api.createProject({ name: u.place });
      if (u.full_name) await api.linkRepo(p.id, u.full_name).catch(() => {});
      onChanged();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="pt-1.5">
      <p className="eyebrow mb-1">Time by evidence</p>
      <ul className="space-y-1">
        {allocations.map((a) => (
          <li
            key={`${a.project_id}-${a.evidenced}`}
            className="flex items-baseline gap-2.5 t-caption text-faint"
          >
            <span className={a.project_id === session.project_id ? "text-dim" : "font-medium text-dim"}>
              {a.name}
            </span>
            <span className="tnum shrink-0 text-dim">{total(a.seconds)}</span>
            <span className="shrink-0">{reasonLabel(a)}</span>
          </li>
        ))}
        {unresolved.map((u) => (
          <li key={u.place} className="flex flex-wrap items-baseline gap-2.5 t-caption text-faint">
            <span className="font-mono">{u.place}</span>
            <span className="tnum shrink-0">{total(u.seconds)}</span>
            <span className="shrink-0">{u.ambiguous ? "two projects claim this" : "no project"}</span>
            {!u.ambiguous && (
              <button onClick={() => void createFrom(u)} disabled={busy} className="btn-bare">
                Create “{u.place}”
              </button>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** True when any evidenced minute resolved away from the declared project. */
function drifts(a: SessionAttributionT | undefined, declaredProjectID: string): boolean {
  return !!a?.allocations.some((x) => x.evidenced && x.project_id !== declaredProjectID);
}

/** "12 Aug " when the stretch is from another day; nothing when today's. */
function clusterDayLabel(iso: string): string {
  const d = new Date(iso);
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  if (d >= today) return "";
  return d.toLocaleDateString([], { day: "numeric", month: "short" }) + " ";
}

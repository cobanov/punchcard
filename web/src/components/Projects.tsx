import { useEffect, useState, type CSSProperties } from "react";
import { api, type Project, type Repo } from "../lib/api";
import { money } from "../lib/format";

/**
 * Projects: what time is booked against.
 *
 * One line per project, everything editable where it is displayed. The old
 * screen edited a rate through window.prompt(), which was a placeholder wearing
 * a UI's clothes — clicking the rate now turns it into an input in place, Enter
 * saves, Escape walks away. Repositories live behind the row, expanded on
 * click, so the list stays one line tall and scannable.
 *
 * Rates are typed in whole currency units — 2500, not 250000 — because that is
 * how a person says them. The conversion to minor units happens here at the
 * edge; nothing downstream sees a decimal, and no rate stays distinct from a
 * rate of zero all the way through.
 */

export function Projects({ projects, onChange }: { projects: Project[]; onChange: () => void }) {
  const [open, setOpen] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const run = async (work: () => Promise<unknown>) => {
    try {
      await work();
      setError(null);
      onChange();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <div>
      {error && (
        <p className="mb-3 rounded-md border-l-2 border-punch bg-card px-3 py-2 text-dim">
          {error}
        </p>
      )}

      <div
        className="panel"
        style={{ "--tbl-cols": "minmax(0, 1fr) 9rem 7rem 4rem" } as CSSProperties}
      >
        <div className="tbl-head">
          <span>Project</span>
          <span>Client</span>
          <span className="text-right">Rate</span>
          <span />
        </div>
        <ul className="divide-y divide-line">
          {projects.map((project) => (
            <li key={project.id}>
              <div className="tbl-row">
                {/* The name is the disclosure control on its own. It used to be
                    one button wrapping both name and client, which meant those
                    two could not sit in separate grid tracks and the row drifted
                    out of line with its header. */}
                <button
                  onClick={() => setOpen(open === project.id ? null : project.id)}
                  aria-expanded={open === project.id}
                  className="truncate text-left font-medium transition-colors hover:text-punch"
                >
                  {project.name}
                </button>
                <span className="truncate text-dim">{project.client || "—"}</span>
                <RateCell project={project} onSave={(body) => run(() => api.updateProject(project.id, body))} />
                <button
                  onClick={() =>
                    confirm(`Archive ${project.name}? Its recorded time is kept.`) &&
                    run(() => api.deleteProject(project.id))
                  }
                  aria-label={`Archive ${project.name}`}
                  className="btn-bare t-caption text-right"
                >
                  archive
                </button>
              </div>
              {open === project.id && <RepoRow projectID={project.id} onError={setError} />}
            </li>
          ))}
          {!projects.length && (
            <li className="px-3 py-6 text-center text-faint">No projects yet — add one below.</li>
          )}
        </ul>
      </div>

      <NewProject onDone={onChange} onError={setError} />
    </div>
  );
}

/** The rate, edited where it is shown. */
function RateCell({
  project,
  onSave,
}: {
  project: Project;
  onSave: (body: Record<string, unknown>) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");

  if (!editing) {
    const shown =
      project.billable && project.hourly_rate_cents != null
        ? `${money(project.hourly_rate_cents, project.currency)}/h`
        : "—";
    return (
      <button
        onClick={() => {
          setValue(
            project.hourly_rate_cents == null ? "" : (project.hourly_rate_cents / 100).toFixed(2),
          );
          setEditing(true);
        }}
        title="Change the hourly rate"
        className="tbl-num t-body text-dim transition-colors hover:text-text"
      >
        {shown}
      </button>
    );
  }

  const save = () => {
    setEditing(false);
    const typed = value.trim();
    // Empty clears the rate entirely — "not costed" is a real state, and it is
    // not the same state as costed at zero.
    onSave(
      typed === ""
        ? { clear_hourly_rate: true }
        : { hourly_rate_cents: Math.round(parseFloat(typed) * 100) },
    );
  };

  return (
    <input
      autoFocus
      value={value}
      onChange={(e) => setValue(e.target.value)}
      onBlur={save}
      onKeyDown={(e) => {
        if (e.key === "Enter") save();
        if (e.key === "Escape") setEditing(false);
      }}
      inputMode="decimal"
      aria-label={`Hourly rate for ${project.name}, in ${project.currency}`}
      placeholder="empty = none"
      className="field tbl-num w-full py-0.5 t-body"
    />
  );
}

/** The repositories behind a row. Linking stays optional and says so — the
 *  scanner finds pushed-to repositories on its own; a link only sharpens the
 *  guess for unmatched commits. */
function RepoRow({ projectID, onError }: { projectID: string; onError: (m: string) => void }) {
  const [repos, setRepos] = useState<Repo[] | null>(null);
  const [value, setValue] = useState("");

  const reload = () => {
    api
      .repos(projectID)
      .then(setRepos)
      .catch(() => setRepos([]));
  };
  useEffect(reload, [projectID]);

  return (
    <div className="flex flex-wrap items-center gap-2 border-t border-line/60 bg-ink/40 px-3 py-2">
      {repos === null ? (
        <span className="skeleton h-4 w-32" />
      ) : (
        repos.map((repo) => (
          <span
            key={repo.id}
            className="inline-flex items-center gap-1.5 rounded bg-raise px-1.5 py-0.5 font-mono t-caption text-dim"
          >
            {repo.full_name}
            <button
              onClick={() =>
                api
                  .unlinkRepo(projectID, repo.id)
                  .then(reload)
                  .catch((e) => onError((e as Error).message))
              }
              aria-label={`Unlink ${repo.full_name}`}
              className="btn-bare"
            >
              ×
            </button>
          </span>
        ))
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          const name = value.trim();
          if (!name) return;
          api
            .linkRepo(projectID, name)
            .then(() => {
              setValue("");
              reload();
            })
            .catch((err) => onError((err as Error).message));
        }}
        className="flex items-center gap-1.5"
      >
        <input
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="owner/repo"
          aria-label="Link a repository"
          className="field w-36 py-0.5 font-mono t-caption"
        />
        <button className="btn-bare t-caption">link</button>
      </form>
      <span className="ml-auto t-caption text-faint">
        optional — punchcard finds the repositories you push to on its own
      </span>
    </div>
  );
}

function NewProject({ onDone, onError }: { onDone: () => void; onError: (m: string) => void }) {
  const [name, setName] = useState("");
  const [client, setClient] = useState("");
  const [rate, setRate] = useState("");
  const [currency, setCurrency] = useState("TRY");

  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        if (!name.trim()) return;
        try {
          await api.createProject({
            name: name.trim(),
            client: client.trim() || undefined,
            currency,
            ...(rate.trim() ? { hourly_rate_cents: Math.round(parseFloat(rate) * 100) } : {}),
          });
          setName("");
          setClient("");
          setRate("");
          onDone();
        } catch (err) {
          onError((err as Error).message);
        }
      }}
      className="mt-3 flex flex-wrap items-center gap-2"
    >
      <input
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="New project"
        aria-label="Project name"
        required
        className="field min-w-40 flex-1 py-1.5"
      />
      <input
        value={client}
        onChange={(e) => setClient(e.target.value)}
        placeholder="Client"
        aria-label="Client"
        className="field w-36 py-1.5"
      />
      <input
        value={rate}
        onChange={(e) => setRate(e.target.value)}
        placeholder="Rate / hour"
        aria-label="Hourly rate"
        inputMode="decimal"
        className="field w-28 py-1.5 text-right font-mono"
      />
      <select
        value={currency}
        onChange={(e) => setCurrency(e.target.value)}
        aria-label="Currency"
        className="select py-1.5"
      >
        {["TRY", "USD", "EUR", "GBP"].map((c) => (
          <option key={c}>{c}</option>
        ))}
      </select>
      <button className="btn-primary">Add</button>
    </form>
  );
}

import { useEffect, useState } from "react";
import { api, type Project, type Repo } from "../lib/api";
import { money } from "../lib/format";
import { assignColors, COLOR_NAMES, PALETTE, projectColor } from "../lib/palette";

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
  // Assigned across the whole list, so two projects that hash to the same
  // colour do not both wear it.
  const colors = assignColors(projects);

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

      <div className="panel tbl-projects">
        <div className="tbl-head hidden mid:grid">
          <span className="c-project">Project</span>
          <span className="c-client">Client</span>
          <span className="c-rate text-right">Rate</span>
          <span className="c-act" />
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
                  className="c-project flex min-w-0 items-center gap-2 text-left"
                >
                  <span
                    className="size-2 shrink-0 rounded-full"
                    style={{ background: colors.get(project.id) }}
                    aria-hidden
                  />
                  <span className="truncate font-medium transition-colors hover:text-punch">
                    {project.name}
                  </span>
                </button>
                <span className="c-client truncate text-dim">{project.client || "—"}</span>
                <span className="c-rate tbl-num t-body text-dim">
                  {project.hourly_rate_cents != null
                    ? `${money(project.hourly_rate_cents, project.currency)}/h`
                    : "—"}
                </span>
                <button
                  onClick={() => setOpen(open === project.id ? null : project.id)}
                  aria-label={`Edit ${project.name}`}
                  className="c-act btn-bare t-caption text-right"
                >
                  {open === project.id ? "close" : "edit"}
                </button>
              </div>
              {open === project.id && (
                <ProjectEditor
                  project={project}
                  onSave={(body) => run(() => api.updateProject(project.id, body))}
                  onArchive={() =>
                    confirm(`Archive ${project.name}? Its recorded time is kept.`) &&
                    run(() => api.deleteProject(project.id))
                  }
                  onClose={() => setOpen(null)}
                  onError={setError}
                />
              )}
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

/**
 * The project editor.
 *
 * Everything about a project in one place, opened from its own row. It replaces
 * a screen where the only editable thing was the rate — and it was edited by
 * clicking the number itself, which nothing on the row said you could do. A
 * project's name and client were not editable at all once created.
 *
 * Save stays disabled until something is actually different, so pressing it
 * always means something happened, and archiving sits at the far end behind a
 * confirmation rather than next to the fields.
 */
function ProjectEditor({
  project,
  onSave,
  onArchive,
  onClose,
  onError,
}: {
  project: Project;
  onSave: (body: Record<string, unknown>) => void;
  onArchive: () => void;
  onClose: () => void;
  onError: (m: string) => void;
}) {
  const [name, setName] = useState(project.name);
  const [client, setClient] = useState(project.client ?? "");
  const [color, setColor] = useState(project.color ?? "");
  // Rates are typed in whole currency units — 2500, not 250000 — because that
  // is how a person says them. The conversion happens at this edge.
  const [rate, setRate] = useState(
    project.hourly_rate_cents == null ? "" : (project.hourly_rate_cents / 100).toFixed(2),
  );
  const [currency, setCurrency] = useState(project.currency);

  const originalRate =
    project.hourly_rate_cents == null ? "" : (project.hourly_rate_cents / 100).toFixed(2);
  const dirty =
    name !== project.name ||
    client !== (project.client ?? "") ||
    color !== (project.color ?? "") ||
    rate.trim() !== originalRate ||
    currency !== project.currency;

  const save = () => {
    if (!name.trim()) return;
    const typed = rate.trim();
    onSave({
      name: name.trim(),
      client: client.trim(),
      // An empty string clears the colour; the API distinguishes it from absent.
      color,
      currency,
      // Empty clears the rate entirely — "not costed" is a real state, and it is
      // not the same state as costed at zero.
      ...(typed === ""
        ? { clear_hourly_rate: true }
        : { hourly_rate_cents: Math.round(parseFloat(typed) * 100) }),
    });
    onClose();
  };

  return (
    <div className="space-y-3 border-t border-line bg-ink/40 px-3 py-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <Labelled label="Name">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && save()}
            aria-label="Project name"
            className="field w-full py-1"
          />
        </Labelled>
        <Labelled label="Client">
          <input
            value={client}
            onChange={(e) => setClient(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && save()}
            placeholder="none"
            aria-label="Client"
            className="field w-full py-1"
          />
        </Labelled>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <Labelled label="Rate" hint="per hour — empty means not costed">
          <div className="flex gap-2">
            <input
              value={rate}
              onChange={(e) => setRate(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && save()}
              inputMode="decimal"
              placeholder="—"
              aria-label="Hourly rate"
              className="field tbl-num min-w-0 flex-1 py-1"
            />
            <select
              value={currency}
              onChange={(e) => setCurrency(e.target.value)}
              aria-label="Currency"
              className="select py-1"
            >
              {["TRY", "USD", "EUR", "GBP"].map((c) => (
                <option key={c}>{c}</option>
              ))}
            </select>
          </div>
        </Labelled>
        <Labelled label="Colour" hint="how this project reads in analytics">
          <Swatches value={color} onChange={setColor} projectID={project.id} />
        </Labelled>
      </div>

      <RepoRow projectID={project.id} onError={onError} />

      <div className="flex items-center gap-2 border-t border-line/60 pt-3">
        <button onClick={save} disabled={!dirty || !name.trim()} className="btn-primary py-1">
          Save
        </button>
        <button onClick={onClose} className="btn-ghost">
          Cancel
        </button>
        <button
          onClick={onArchive}
          className="btn-bare ml-auto t-caption hover:text-punch"
        >
          Archive project
        </button>
      </div>
    </div>
  );
}

/** A field and its name, so the editor reads as a form rather than a row of
 *  unlabelled boxes. */
function Labelled({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <span className="eyebrow mb-1 block">{label}</span>
      {children}
      {hint && <span className="t-caption mt-1 block text-faint">{hint}</span>}
    </label>
  );
}

/**
 * The colour choice.
 *
 * Eight swatches and an "auto". Auto is the default and is not an absence: an
 * unset project still gets a stable colour derived from its id, so the charts
 * read as separate projects before anybody configures anything. The swatch for
 * auto therefore shows the colour it would actually be, not a grey blank.
 */
function Swatches({
  value,
  onChange,
  projectID,
}: {
  value: string;
  onChange: (c: string) => void;
  projectID: string;
}) {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <button
        onClick={() => onChange("")}
        aria-pressed={value === ""}
        title="Automatic"
        className={
          value === ""
            ? "flex size-6 items-center justify-center rounded-full ring-2 ring-text ring-offset-2 ring-offset-ink"
            : "flex size-6 items-center justify-center rounded-full"
        }
      >
        <span
          className="size-3.5 rounded-full opacity-60"
          style={{ background: projectColor(undefined, projectID) }}
        />
      </button>
      {COLOR_NAMES.map((c) => (
        <button
          key={c}
          onClick={() => onChange(c)}
          aria-pressed={value === c}
          aria-label={c}
          title={c}
          className={
            value === c
              ? "size-6 rounded-full ring-2 ring-text ring-offset-2 ring-offset-ink"
              : "swatch size-6 rounded-full"
          }
          style={{ background: PALETTE[c] }}
        />
      ))}
    </div>
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

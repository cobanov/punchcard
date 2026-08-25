import { useEffect, useState } from "react";
import { api, type Project, type Repo } from "../lib/api";
import { money } from "../lib/format";

/**
 * Projects: what time is booked against.
 *
 * A rate is typed in whole currency units — 2500, not 250000 — because that is
 * how a person says it. The conversion to minor units happens here, at the edge,
 * and nothing downstream ever sees a decimal. Making someone type the database's
 * representation would be exporting the schema as the interface.
 */

export function Projects({ projects, onChange }: { projects: Project[]; onChange: () => void }) {
  const [repos, setRepos] = useState<Record<string, Repo[]>>({});
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    Promise.all(
      projects.map(async (p) => [p.id, await api.repos(p.id).catch(() => [])] as const),
    ).then((pairs) => {
      if (!cancelled) setRepos(Object.fromEntries(pairs));
    });
    return () => {
      cancelled = true;
    };
  }, [projects]);

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
        <p className="mb-4 rounded-md border-l-2 border-punch bg-raise px-3 py-2 text-dim">
          {error}
        </p>
      )}

      <ul className="divide-y divide-line border-y border-line">
        {projects.map((project) => (
          <li key={project.id} className="py-3">
            <div className="flex items-baseline gap-3">
              <span className="w-44 shrink-0 truncate font-medium">{project.name}</span>
              <span className="min-w-0 flex-1 truncate text-dim">{project.client}</span>
              <span className="shrink-0 font-mono text-[12px] tabular-nums text-dim">
                {project.billable && project.hourly_rate_cents != null
                  ? `${money(project.hourly_rate_cents, project.currency)}/h`
                  : "—"}
              </span>
              <RateButton project={project} onChange={onChange} />
              <button
                onClick={() =>
                  confirm(`Archive ${project.name}? Its recorded time is kept.`) &&
                  run(() => api.deleteProject(project.id))
                }
                className="shrink-0 text-[12px] text-faint hover:text-punch"
              >
                archive
              </button>
            </div>

            <div className="mt-2 flex flex-wrap items-center gap-2 pl-44">
              {(repos[project.id] ?? []).map((repo) => (
                <span
                  key={repo.id}
                  className="inline-flex items-center gap-1.5 rounded bg-raise px-1.5 py-0.5 font-mono text-[11px] text-dim"
                >
                  {repo.full_name}
                  <button
                    onClick={() => run(() => api.unlinkRepo(project.id, repo.id))}
                    aria-label={`Unlink ${repo.full_name}`}
                    className="text-faint hover:text-punch"
                  >
                    ×
                  </button>
                </span>
              ))}
              <LinkRepo projectID={project.id} onDone={onChange} onError={setError} />
            </div>
          </li>
        ))}
      </ul>

      <NewProject onDone={onChange} onError={setError} />

      <p className="mt-3 text-[12px] text-faint">
        Linking a repository is optional — punchcard finds the ones you push to on its own.
        Link one when you want it to guess which project a stretch of unmatched commits belongs to.
      </p>
    </div>
  );
}

function RateButton({ project, onChange }: { project: Project; onChange: () => void }) {
  return (
    <button
      onClick={async () => {
        const typed = prompt(
          `Hourly rate for ${project.name}, in whole ${project.currency}. Empty to remove it.`,
          project.hourly_rate_cents == null ? "" : (project.hourly_rate_cents / 100).toFixed(2),
        );
        if (typed === null) return;
        const body =
          typed.trim() === ""
            ? { clear_hourly_rate: true }
            : { hourly_rate_cents: Math.round(parseFloat(typed) * 100) };
        await api.updateProject(project.id, body).catch(() => {});
        onChange();
      }}
      className="shrink-0 text-[12px] text-faint hover:text-punch"
    >
      rate
    </button>
  );
}

function LinkRepo({
  projectID,
  onDone,
  onError,
}: {
  projectID: string;
  onDone: () => void;
  onError: (m: string) => void;
}) {
  const [value, setValue] = useState("");
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        if (!value.trim()) return;
        try {
          await api.linkRepo(projectID, value.trim());
          setValue("");
          onDone();
        } catch (err) {
          onError((err as Error).message);
        }
      }}
      className="flex items-center gap-1"
    >
      <input
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder="owner/repo"
        aria-label="Link a repository"
        className="w-32 rounded bg-raise px-1.5 py-0.5 font-mono text-[11px] outline-none placeholder:text-faint"
      />
      <button className="text-[11px] text-faint hover:text-punch">link</button>
    </form>
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
            // No rate is not a rate of zero: an unpriced project must not be
            // sent as costed at nothing.
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
      className="mt-4 flex flex-wrap items-center gap-2"
    >
      <input
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="New project"
        aria-label="Project name"
        required
        className="min-w-40 flex-1 rounded-md border border-line bg-card px-2.5 py-1.5 outline-none placeholder:text-faint"
      />
      <input
        value={client}
        onChange={(e) => setClient(e.target.value)}
        placeholder="Client"
        aria-label="Client"
        className="w-36 rounded-md border border-line bg-card px-2.5 py-1.5 outline-none placeholder:text-faint"
      />
      <input
        value={rate}
        onChange={(e) => setRate(e.target.value)}
        placeholder="Rate / hour"
        aria-label="Hourly rate"
        inputMode="decimal"
        className="w-28 rounded-md border border-line bg-card px-2.5 py-1.5 text-right font-mono outline-none placeholder:text-faint"
      />
      <select
        value={currency}
        onChange={(e) => setCurrency(e.target.value)}
        aria-label="Currency"
        className="rounded-md border border-line bg-card px-2 py-1.5 outline-none"
      >
        {["TRY", "USD", "EUR", "GBP"].map((c) => (
          <option key={c}>{c}</option>
        ))}
      </select>
      <button className="rounded-md bg-punch px-4 py-1.5 font-medium text-ink hover:brightness-110">
        Add
      </button>
    </form>
  );
}

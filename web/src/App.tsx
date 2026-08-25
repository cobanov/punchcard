import { useCallback, useEffect, useState } from "react";
import {
  api,
  NotSignedIn,
  signInURL,
  type Cluster,
  type Commit,
  type GitHubStatus,
  type Project,
  type ProjectTotal,
  type Session,
} from "./lib/api";
import { daysAgo, money, startOfToday, total } from "./lib/format";
import { DayCard } from "./components/DayCard";
import { Projects } from "./components/Projects";
import { TimerBar } from "./components/TimerBar";

type View = "today" | "projects" | "reports";

export function App() {
  const [signedIn, setSignedIn] = useState<boolean | null>(null);
  const [view, setView] = useState<View>("today");
  const [projects, setProjects] = useState<Project[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [commits, setCommits] = useState<Record<string, Commit[]>>({});
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [github, setGithub] = useState<GitHubStatus | null>(null);
  const [totals, setTotals] = useState<ProjectTotal[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(Date.now());

  const current = sessions.find((s) => s.running) ?? null;
  const projectName = useCallback(
    (id: string) => projects.find((p) => p.id === id)?.name ?? "—",
    [projects],
  );

  const load = useCallback(async () => {
    try {
      const [loadedProjects, loadedSessions, loadedGithub] = await Promise.all([
        api.projects(),
        api.sessions(startOfToday(), new Date(Date.now() + 60_000)),
        api.github(),
      ]);
      setSignedIn(true);
      setProjects(loadedProjects);
      setSessions(loadedSessions);
      setGithub(loadedGithub);
      setError(null);

      // Unmatched commits over the last week: the same span the server
      // re-scans, and long enough that a timer forgotten on Friday is still
      // recoverable on Monday. Only today's are drawn on the card.
      api
        .unmatched(daysAgo(7), new Date())
        .then((all) => setClusters(all.filter((c) => new Date(c.from) >= startOfToday())))
        .catch(() => setClusters([]));

      // Commit lists are per session. One failing must not blank the day.
      const pairs = await Promise.all(
        loadedSessions
          .filter((s) => !s.running)
          .map(async (s) => [s.id, await api.commits(s.id).catch(() => [])] as const),
      );
      setCommits(Object.fromEntries(pairs));
    } catch (e) {
      if (e instanceof NotSignedIn) {
        setSignedIn(false);
        return;
      }
      setError((e as Error).message);
    }
  }, []);

  const loadTotals = useCallback(async () => {
    try {
      setTotals((await api.summary(daysAgo(30), new Date())).projects);
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);
  useEffect(() => {
    if (view === "reports") void loadTotals();
  }, [view, loadTotals]);

  // A running band has to grow. The card is drawn from clock time, so it needs
  // a heartbeat of its own.
  useEffect(() => {
    if (!current) return;
    const id = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(id);
  }, [current]);

  // The server's event stream: a timer started from the CLI or the menu bar
  // appears here at once rather than on the next poll. Polling stays as the
  // fallback for when the stream drops.
  useEffect(() => {
    if (!signedIn) return;
    const source = new EventSource("/v1/events/stream");
    source.onmessage = () => void load();
    const poll = setInterval(() => void load(), 60_000);
    return () => {
      source.close();
      clearInterval(poll);
    };
  }, [signedIn, load]);

  const act = async (work: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await work();
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      await load();
    }
  };

  if (signedIn === null) {
    return <div className="p-16 text-center text-faint">Loading…</div>;
  }
  if (!signedIn) {
    return <SignIn />;
  }

  return (
    <div className="mx-auto max-w-3xl px-5 pb-24 pt-8">
      <header className="mb-6 flex items-baseline gap-6 border-b border-line pb-3">
        <span className="font-medium tracking-tight">punchcard</span>
        <nav className="flex gap-4">
          {(["today", "projects", "reports"] as const).map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              className={
                view === v
                  ? "border-b border-punch pb-3 text-text"
                  : "pb-3 text-dim hover:text-text"
              }
            >
              {v[0]!.toUpperCase() + v.slice(1)}
            </button>
          ))}
        </nav>
        <span className="ml-auto font-mono text-[12px] text-faint">
          {github?.login ? `@${github.login}` : ""}
        </span>
      </header>

      {error && (
        <p className="mb-4 rounded-md border-l-2 border-punch bg-raise px-3 py-2 text-dim">
          {error}
        </p>
      )}

      {view === "today" && (
        <>
          <TimerBar
            current={current}
            projects={projects}
            projectName={projectName}
            onStart={(id, note) => act(() => api.start(id, note))}
            onStop={() => act(() => api.stop(current!.id))}
            busy={busy}
          />
          <div className="mt-8">
            <DayCard
              sessions={sessions}
              commits={commits}
              clusters={clusters}
              projects={projects}
              now={now}
              onRecover={(cluster) =>
                void act(() =>
                  api.recover({
                    project_id: cluster.suggested_project_id ?? projects[0]!.id,
                    from: cluster.from,
                    to: cluster.to,
                    note: cluster.suggested_note ?? "",
                  }),
                )
              }
            />
          </div>
          <DayFooter sessions={sessions} commits={commits} />
          <GitHubNote status={github} />
        </>
      )}

      {view === "projects" && <Projects projects={projects} onChange={load} />}

      {view === "reports" && <Reports totals={totals} />}
    </div>
  );
}

function DayFooter({
  sessions,
  commits,
}: {
  sessions: Session[];
  commits: Record<string, Commit[]>;
}) {
  const finished = sessions.filter((s) => !s.running);
  if (!finished.length) return null;
  const seconds = finished.reduce((sum, s) => sum + s.seconds, 0);
  const count = finished.reduce((sum, s) => sum + (commits[s.id]?.length ?? 0), 0);
  return (
    <div className="mt-6 flex items-baseline justify-between border-t border-line pt-3 text-dim">
      <span>{finished.length} sessions</span>
      <span className="font-mono tabular-nums">
        {total(seconds)} · {count} commits
      </span>
    </div>
  );
}

function Reports({ totals }: { totals: ProjectTotal[] }) {
  if (!totals.length) {
    return <p className="py-16 text-center text-dim">Nothing recorded in the last 30 days.</p>;
  }
  const seconds = totals.reduce((sum, t) => sum + t.seconds, 0);
  return (
    <div>
      <p className="mb-3 text-[12px] uppercase tracking-wider text-faint">Last 30 days</p>
      <ul className="divide-y divide-line border-y border-line">
        {totals.map((t) => (
          <li key={t.project_id} className="flex items-baseline gap-3 py-3">
            <span className="w-44 shrink-0 truncate font-medium">{t.name}</span>
            <span className="min-w-0 flex-1 truncate text-dim">{t.client}</span>
            <span className="w-20 shrink-0 text-right font-mono tabular-nums text-dim">
              {total(t.seconds)}
            </span>
            <span className="w-32 shrink-0 text-right font-mono tabular-nums">
              {t.amount_cents == null ? (
                <span className="text-faint">—</span>
              ) : (
                money(t.amount_cents, t.currency)
              )}
            </span>
          </li>
        ))}
      </ul>
      <div className="flex items-baseline justify-between pt-3 text-dim">
        <span>Total</span>
        <span className="font-mono tabular-nums">{total(seconds)}</span>
      </div>
      <a
        href="/v1/reports/export.csv"
        className="mt-4 inline-block text-[12px] text-faint hover:text-punch"
      >
        Download CSV
      </a>
    </div>
  );
}

function GitHubNote({ status }: { status: GitHubStatus | null }) {
  if (!status) return null;
  if (!status.connected) {
    return (
      <p className="mt-6 text-dim">
        GitHub is not connected, so commits will not be attached.{" "}
        <a href={signInURL} className="text-punch hover:underline">
          Connect
        </a>
      </p>
    );
  }
  if (status.last_error) {
    return (
      <p className="mt-6 rounded-md border-l-2 border-punch bg-raise px-3 py-2 text-dim">
        GitHub: {status.last_error}
      </p>
    );
  }
  return null;
}

/** The door to the app.
 *
 *  Dark whatever the system says, because it is the same door as the front of
 *  the site: someone arriving from the landing page should not watch the
 *  product change identity on the way in. The punches are the only thing that
 *  needs to be here — they are what the sign-in is for. */
function SignIn() {
  return (
    <div className="fixed inset-0 flex items-center justify-center bg-[#08090b] px-6 text-[#ecedf0]">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex gap-1.5" aria-hidden>
          {[1, 0, 1, 1, 0, 1, 0, 0, 1, 1, 1, 0].map((on, i) => (
            <span
              key={i}
              className={
                on
                  ? "h-6 w-2 rounded-[2px] bg-[#e9973f]"
                  : "h-6 w-2 rounded-[2px] bg-[#1e2127]"
              }
            />
          ))}
        </div>
        <h1 className="text-xl font-semibold tracking-tight">punchcard</h1>
        <p className="mt-1.5 text-[#979da9]">
          Time tracking for developers, with the commits attached.
        </p>
        <a
          href={signInURL}
          className="mt-7 block rounded-md bg-[#ecedf0] px-5 py-2.5 text-center font-medium text-[#08090b] transition hover:opacity-90 active:scale-[0.99]"
        >
          Sign in with GitHub
        </a>
        <p className="mt-4 text-[12px] leading-relaxed text-[#5b6068]">
          One authorization signs you in and lets punchcard read the commits behind your work.
          Nothing is written to your repositories.
        </p>
      </div>
    </div>
  );
}

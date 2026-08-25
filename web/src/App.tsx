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
import {
  addDays,
  dayName,
  daysAgo,
  isToday,
  money,
  startOfToday,
  toDateInput,
  total,
} from "./lib/format";
import { SessionList, UnmatchedList } from "./components/SessionList";
import { Projects } from "./components/Projects";
import { TimerBar } from "./components/TimerBar";

type View = "today" | "projects" | "reports";

export function App() {
  const [signedIn, setSignedIn] = useState<boolean | null>(null);
  const [view, setView] = useState<View>("today");
  // The day on screen. Sessions, commits and the stats strip all describe it;
  // the timer and the week number are global and fetched on their own.
  const [day, setDay] = useState<Date>(startOfToday());
  const [current, setCurrent] = useState<Session | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [commits, setCommits] = useState<Record<string, Commit[]>>({});
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [github, setGithub] = useState<GitHubStatus | null>(null);
  const [weekSeconds, setWeekSeconds] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const projectName = useCallback(
    (id: string) => projects.find((p) => p.id === id)?.name ?? "—",
    [projects],
  );

  const load = useCallback(async () => {
    try {
      const [loadedProjects, loadedCurrent, loadedSessions, loadedGithub] = await Promise.all([
        api.projects(),
        // The running timer is fetched on its own, not derived from the day's
        // sessions — browsing yesterday must not make a running timer vanish
        // from the bar.
        api.current(),
        api.sessions(day, addDays(day, 1)),
        api.github(),
      ]);
      setSignedIn(true);
      setProjects(loadedProjects);
      setCurrent(loadedCurrent);
      setSessions(loadedSessions);
      setGithub(loadedGithub);
      setError(null);

      // The week number in the stats strip. Fetched with the page rather than
      // behind a tab, because "how is the week going" is a glance, not a visit.
      api
        .summary(daysAgo(7), new Date())
        .then((r) => setWeekSeconds(r.projects.reduce((sum, t) => sum + t.seconds, 0)))
        .catch(() => {});

      // Unmatched commits over the last week — the span the server re-scans,
      // and long enough that Friday's forgotten timer survives the weekend.
      api
        .unmatched(daysAgo(7), new Date())
        .then(setClusters)
        .catch(() => setClusters([]));

      // Commit lists are per session; one failing must not blank the day.
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
  }, [day]);

  useEffect(() => {
    void load();
  }, [load]);

  // Day navigation from the keyboard: ← → move a day, t returns to today.
  // Only when nothing is focused — arrows inside an input mean the input.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement).tagName;
      if (["INPUT", "TEXTAREA", "SELECT"].includes(tag)) return;
      if (e.key === "ArrowLeft") setDay((d) => addDays(d, -1));
      if (e.key === "ArrowRight") setDay((d) => (isToday(d) ? d : addDays(d, 1)));
      if (e.key === "t") setDay(startOfToday());
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // The server's event stream: a timer started from the CLI or the menu bar
  // shows up here at once. Polling stays as the fallback for a dropped stream.
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

  if (signedIn === null) return <LoadingScreen />;
  if (!signedIn) return <SignIn />;

  return (
    <div className="mx-auto max-w-2xl px-4 pb-20 pt-6">
      <header className="mb-5 flex items-center gap-4">
        <span className="font-semibold tracking-tight">punchcard</span>
        <nav
          className="flex gap-0.5 rounded-lg border border-line bg-card p-0.5"
          aria-label="Views"
        >
          {(["today", "projects", "reports"] as const).map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              aria-current={view === v ? "page" : undefined}
              className={
                view === v
                  ? "rounded-md bg-raise px-3 py-1 text-text"
                  : "rounded-md px-3 py-1 text-dim transition-colors hover:text-text"
              }
            >
              {v[0]!.toUpperCase() + v.slice(1)}
            </button>
          ))}
        </nav>
        <span className="ml-auto font-mono text-[11px] text-faint">
          {github?.login ? `@${github.login}` : ""}
        </span>
      </header>

      {error && (
        <p className="mb-3 rounded-md border-l-2 border-punch bg-card px-3 py-2 text-dim">
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

          <StatsStrip day={day} sessions={sessions} commits={commits} weekSeconds={weekSeconds} />

          <section className="panel mt-3 overflow-hidden" aria-label="Sessions">
            <DayNav day={day} onChange={setDay} />
            <SessionList
              sessions={sessions}
              commits={commits}
              projects={projects}
              busy={busy}
              onSave={(id, body) => void act(() => api.updateSession(id, body))}
              onDelete={(id) => void act(() => api.deleteSession(id))}
            />
          </section>

          <UnmatchedList
            clusters={clusters}
            projects={projects}
            busy={busy}
            onRecover={(cluster, projectID) =>
              void act(() =>
                api.recover({
                  project_id: projectID,
                  from: cluster.from,
                  to: cluster.to,
                  note: cluster.suggested_note ?? "",
                }),
              )
            }
          />

          <GitHubNote status={github} />
        </>
      )}

      {view === "projects" && <Projects projects={projects} onChange={load} />}
      {view === "reports" && <Reports />}
    </div>
  );
}

/**
 * The numbers a glance is for: today, the week, the evidence. On the main
 * screen rather than behind the Reports tab, because checking them should cost
 * a look, not a navigation.
 */
function StatsStrip({
  day,
  sessions,
  commits,
  weekSeconds,
}: {
  day: Date;
  sessions: Session[];
  commits: Record<string, Commit[]>;
  weekSeconds: number | null;
}) {
  const finished = sessions.filter((s) => !s.running);
  const daySeconds = finished.reduce((sum, s) => sum + s.seconds, 0);
  const commitCount = finished.reduce((sum, s) => sum + (commits[s.id]?.length ?? 0), 0);

  const item = (label: string, value: string) => (
    <span className="flex items-baseline gap-1.5">
      <span className="text-[11px] text-faint">{label}</span>
      <span className="tnum font-mono text-[12px] text-dim">{value}</span>
    </span>
  );

  return (
    <div className="mt-3 flex flex-wrap items-baseline gap-x-5 gap-y-1 px-1">
      {item(isToday(day) ? "today" : dayName(day), total(daySeconds))}
      {item("week", weekSeconds === null ? "—" : total(weekSeconds))}
      {item("commits", String(commitCount))}
    </div>
  );
}

/**
 * Browsing the calendar. Arrows walk a day at a time (also ← → on the
 * keyboard, t for today), the native date input jumps anywhere — no picker
 * library, the platform already has one and it matches the OS.
 */
function DayNav({ day, onChange }: { day: Date; onChange: (d: Date) => void }) {
  const today = isToday(day);
  return (
    <header className="flex items-center gap-1.5 border-b border-line px-2 py-1.5">
      <button
        onClick={() => onChange(addDays(day, -1))}
        aria-label="Previous day"
        className="btn-bare px-1.5 text-[14px] leading-none"
      >
        ‹
      </button>
      <button
        onClick={() => onChange(addDays(day, 1))}
        disabled={today}
        aria-label="Next day"
        className="btn-bare px-1.5 text-[14px] leading-none disabled:opacity-30"
      >
        ›
      </button>
      <input
        type="date"
        value={toDateInput(day)}
        max={toDateInput(new Date())}
        onChange={(e) => {
          // An empty value is the input being cleared, not a day.
          if (e.target.value) onChange(new Date(`${e.target.value}T00:00:00`));
        }}
        aria-label="Go to date"
        className="field border-0 bg-transparent py-0.5 font-mono text-[12px] text-dim"
      />
      <span className="text-[12px] font-medium text-text">
        {today ? "Today" : dayName(day)}
      </span>
      {!today && (
        <button onClick={() => onChange(startOfToday())} className="btn-ghost ml-auto py-0.5 text-[11px]">
          Today
        </button>
      )}
    </header>
  );
}

/** Range totals. Data is fetched here rather than upstream: the tab owns its
 *  own question, and the main screen never pays for it. */
function Reports() {
  const [days, setDays] = useState<7 | 30>(7);
  const [totals, setTotals] = useState<ProjectTotal[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setTotals(null);
    api
      .summary(daysAgo(days), new Date())
      .then((r) => setTotals(r.projects))
      .catch((e) => setError((e as Error).message));
  }, [days]);

  const from = daysAgo(days).toISOString();

  return (
    <div>
      <div className="mb-3 flex w-fit items-center gap-0.5 rounded-lg border border-line bg-card p-0.5">
        {([7, 30] as const).map((d) => (
          <button
            key={d}
            onClick={() => setDays(d)}
            className={
              days === d
                ? "rounded-md bg-raise px-3 py-1 text-text"
                : "rounded-md px-3 py-1 text-dim transition-colors hover:text-text"
            }
          >
            {d} days
          </button>
        ))}
      </div>

      {error && (
        <p className="mb-3 rounded-md border-l-2 border-punch bg-card px-3 py-2 text-dim">
          {error}
        </p>
      )}

      {totals === null ? (
        <div className="panel space-y-2 p-3">
          <div className="skeleton h-5 w-full" />
          <div className="skeleton h-5 w-4/5" />
          <div className="skeleton h-5 w-3/5" />
        </div>
      ) : totals.length === 0 ? (
        <p className="panel px-3 py-6 text-center text-faint">
          Nothing recorded in the last {days} days.
        </p>
      ) : (
        <>
          <div className="panel overflow-hidden">
            <ul className="divide-y divide-line">
              {totals.map((t) => (
                <li key={t.project_id} className="flex items-baseline gap-3 px-3 py-2">
                  <span className="w-40 shrink-0 truncate font-medium">{t.name}</span>
                  <span className="min-w-0 flex-1 truncate text-dim">{t.client}</span>
                  <span className="tnum w-16 shrink-0 text-right font-mono text-[12px] text-dim">
                    {total(t.seconds)}
                  </span>
                  <span className="tnum w-28 shrink-0 text-right font-mono text-[12px]">
                    {t.amount_cents == null ? (
                      <span className="text-faint">—</span>
                    ) : (
                      money(t.amount_cents, t.currency)
                    )}
                  </span>
                </li>
              ))}
            </ul>
          </div>
          <div className="mt-2 flex items-baseline justify-between px-1 text-dim">
            <a
              href={`/v1/reports/export.csv?from=${encodeURIComponent(from)}`}
              className="text-[12px] text-faint transition-colors hover:text-punch"
            >
              Download CSV
            </a>
            <span className="tnum font-mono text-[12px]">
              {total(totals.reduce((sum, t) => sum + t.seconds, 0))}
            </span>
          </div>
        </>
      )}
    </div>
  );
}

function GitHubNote({ status }: { status: GitHubStatus | null }) {
  if (!status) return null;
  if (!status.connected) {
    return (
      <p className="mt-4 px-1 text-dim">
        GitHub is not connected, so commits will not be attached.{" "}
        <a href={signInURL} className="text-punch hover:underline">
          Connect
        </a>
      </p>
    );
  }
  if (status.last_error) {
    return (
      <p className="mt-4 rounded-md border-l-2 border-punch bg-card px-3 py-2 text-dim">
        GitHub: {status.last_error}
      </p>
    );
  }
  return null;
}

/** The page's shape, before its data. A bare "Loading…" makes the layout jump
 *  when the real thing lands. */
function LoadingScreen() {
  return (
    <div className="mx-auto max-w-2xl px-4 pt-6">
      <div className="mb-5 flex items-center gap-4">
        <span className="font-semibold tracking-tight">punchcard</span>
        <div className="skeleton h-7 w-56" />
      </div>
      <div className="skeleton h-12 w-full" />
      <div className="mt-3 space-y-1">
        <div className="skeleton h-9 w-full" />
        <div className="skeleton h-9 w-full" />
        <div className="skeleton h-9 w-3/4" />
      </div>
    </div>
  );
}

/** The door. Dark whatever the system says — it is the same door as the front
 *  of the site, and the product does not change identity on the way in. */
function SignIn() {
  return (
    <div className="fixed inset-0 flex items-center justify-center bg-ink px-6 text-text">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex gap-1.5" aria-hidden>
          {[1, 0, 1, 1, 0, 1, 0, 0, 1, 1, 1, 0].map((on, i) => (
            <span
              key={i}
              className={on ? "h-6 w-2 rounded-[2px] bg-punch" : "h-6 w-2 rounded-[2px] bg-raise"}
            />
          ))}
        </div>
        <h1 className="text-xl font-semibold tracking-tight">punchcard</h1>
        <p className="mt-1.5 text-dim">
          Time tracking for developers, with the commits attached.
        </p>
        <a href={signInURL} className="btn-primary mt-7 block w-full py-2.5 text-center">
          Sign in with GitHub
        </a>
        <p className="mt-4 text-[12px] leading-relaxed text-faint">
          One authorization signs you in and lets punchcard read the commits behind your work.
          Nothing is written to your repositories.
        </p>
      </div>
    </div>
  );
}

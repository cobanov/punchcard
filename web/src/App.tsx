import { useCallback, useEffect, useState } from "react";
import {
  api,
  NotSignedIn,
  signInURL,
  type Account,
  type Cluster,
  type Commit,
  type GitHubStatus,
  type Project,
  type Session,
} from "./lib/api";
import { addDays, dayName, daysAgo, isToday, startOfToday, toDateInput, total } from "./lib/format";
import { SessionList, UnmatchedList } from "./components/SessionList";
import { Projects } from "./components/Projects";
import { TimerBar } from "./components/TimerBar";
import { Analytics } from "./components/Analytics";
import { Settings } from "./components/Settings";
import { UserMenu } from "./components/UserMenu";

/** Tab order is by how often each is opened, not alphabetically: Today is
 *  every visit, Analytics is weekly, Projects is setup. Settings is not a tab —
 *  it lives in the account menu, because it is not a place you work. */
const TABS = ["today", "analytics", "projects"] as const;
type View = (typeof TABS)[number] | "settings";

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
  const [account, setAccount] = useState<Account | null>(null);
  const [weekSeconds, setWeekSeconds] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const projectName = useCallback(
    (id: string) => projects.find((p) => p.id === id)?.name ?? "—",
    [projects],
  );

  const load = useCallback(async () => {
    try {
      const [loadedProjects, loadedCurrent, loadedSessions, loadedGithub, loadedAccount] =
        await Promise.all([
        api.projects(),
        // The running timer is fetched on its own, not derived from the day's
        // sessions — browsing yesterday must not make a running timer vanish
        // from the bar.
        api.current(),
        api.sessions(day, addDays(day, 1)),
        api.github(),
        api.me(),
      ]);
      setSignedIn(true);
      setProjects(loadedProjects);
      setCurrent(loadedCurrent);
      setSessions(loadedSessions);
      setGithub(loadedGithub);
      setAccount(loadedAccount);
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
    // 4xl, not 2xl. The columns are fixed widths now, so everything the narrower
    // shell took came out of the note — the one column that holds a sentence.
    // At 672px a note truncated after roughly four words.
    <div className="mx-auto max-w-4xl px-4 pb-20 pt-6">
      {/* One row, three groups, aligned on a single centre line. The wordmark
          and the tabs share a baseline; the account sits at the far end. */}
      <header className="mb-5 flex h-8 items-center gap-4">
        <button
          onClick={() => setView("today")}
          className="shrink-0 font-semibold tracking-tight"
        >
          punchcard
        </button>
        <nav className="flex gap-0.5 rounded-lg border border-line bg-card p-0.5" aria-label="Views">
          {TABS.map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              aria-current={view === v ? "page" : undefined}
              className={
                view === v
                  ? "rounded-md bg-raise px-3 py-1 text-text"
                  : "rounded-md px-3 py-1 text-dim transition-colors hover:bg-raise/50 hover:text-text"
              }
            >
              {v[0]!.toUpperCase() + v.slice(1)}
            </button>
          ))}
        </nav>
        {/* The trigger's own padding is pulled outside the container, so the
            account name ends on the same line as the cards below it rather
            than six pixels short of it. The padding stays — it is the hover
            target — it just stops pushing the text out of alignment. */}
        <div className="-mr-1.5 ml-auto">
          <UserMenu
            account={account}
            onSettings={() => setView("settings")}
            onSignOut={() =>
              void api.logout().finally(() => {
                window.location.href = "/";
              })
            }
          />
        </div>
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
            onRecover={(cluster, target) =>
              void act(async () => {
                let projectID: string;
                if ("projectID" in target) {
                  projectID = target.projectID;
                } else {
                  // The commits named the project, so create it and link the
                  // repository they came from — that link is what sharpens the
                  // suggestion the next time this happens.
                  const created = await api.createProject({ name: target.newProject });
                  projectID = created.id;
                  if (target.repo) await api.linkRepo(projectID, target.repo).catch(() => {});
                }
                return api.recover({
                  project_id: projectID,
                  from: cluster.from,
                  to: cluster.to,
                  note: cluster.suggested_note ?? "",
                });
              })
            }
          />

          <GitHubNote status={github} />
        </>
      )}

      {view === "analytics" && <Analytics timezone={account?.timezone ?? "UTC"} />}
      {view === "projects" && <Projects projects={projects} onChange={load} />}
      {view === "settings" && account && (
        <Settings account={account} github={github} onSaved={load} />
      )}
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
      <span className="t-caption text-faint">{label}</span>
      <span className="tnum font-mono t-body text-dim">{value}</span>
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
    // px-3 and py-2, the same as the table rows below, so this bar starts and
    // ends on the same two vertical lines the columns do. The first chevron
    // then pulls its own padding back out with -ml-1.5: the padding is the
    // click target and should stay, but it was pushing the glyph two pixels
    // past where TIME begins, which is exactly the kind of two pixels that
    // reads as "crooked" without being nameable.
    <header className="flex items-center gap-1.5 border-b border-line px-3 py-2">
      <button
        onClick={() => onChange(addDays(day, -1))}
        aria-label="Previous day"
        className="btn-bare -ml-1.5 px-1.5 t-body leading-none"
      >
        ‹
      </button>
      <button
        onClick={() => onChange(addDays(day, 1))}
        disabled={today}
        aria-label="Next day"
        className="btn-bare px-1.5 t-body leading-none disabled:opacity-30"
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
        className="field border-0 bg-transparent px-1 py-0.5 font-mono t-body text-dim"
      />
      <span className="t-body font-medium text-text">
        {today ? "Today" : dayName(day)}
      </span>
      {!today && (
        <button onClick={() => onChange(startOfToday())} className="btn-ghost -mr-1 ml-auto py-0.5 t-caption">
          Today
        </button>
      )}
    </header>
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
    <div className="mx-auto max-w-4xl px-4 pt-6">
      <div className="mb-5 flex h-8 items-center gap-4">
        <span className="shrink-0 font-semibold tracking-tight">punchcard</span>
        <div className="skeleton h-7 w-52" />
        <div className="skeleton ml-auto h-6 w-6 rounded-full" />
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
        <p className="mt-4 t-body leading-relaxed text-faint">
          One authorization signs you in and lets punchcard read the commits behind your work.
          Nothing is written to your repositories.
        </p>
      </div>
    </div>
  );
}

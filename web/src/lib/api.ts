/**
 * The punchcard API, typed.
 *
 * Same public API as the CLI and the menu bar app, authenticated the way a
 * browser should: the session cookie, plus the CSRF token the server hands back
 * in a readable cookie. There is no privileged path for this client.
 */

export class NotSignedIn extends Error {
  constructor() {
    super("not signed in");
  }
}

/** The API's RFC 9457 error body. Its `detail` is written for a person to read,
 *  so it is what surfaces — anything this layer invented would be worse. */
export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message);
  }
}

export interface Project {
  id: string;
  name: string;
  client?: string;
  color?: string;
  hourly_rate_cents: number | null;
  currency: string;
  billable: boolean;
  archived_at?: string;
}

export interface Session {
  id: string;
  project_id: string;
  note: string;
  started_at: string;
  ended_at: string | null;
  seconds: number;
  running: boolean;
  source: string;
  commit_sync_state: "pending" | "ok" | "error" | "skipped";
  commit_sync_error?: string;
}

export interface Commit {
  sha: string;
  repo: string;
  message: string;
  committed_at: string;
  url?: string;
}

/**
 * One agent's working interval, as reported by a local hook.
 *
 * Reported is the word that matters. A commit is something punchcard fetched
 * from GitHub itself and can prove; this is a client's account of what it did,
 * which nothing can check. Screens keep the two visibly apart.
 */
export interface AgentRun {
  tool: string;
  started_at: string;
  ended_at: string;
  seconds: number;
  model?: string;
  cwd?: string;
  repo?: string;
  tool_calls?: number;
}

export interface SessionAllocation {
  project_id: string;
  name: string;
  seconds: number;
  evidenced: boolean;
  reason: "linked" | "name" | "declared";
}

export interface UnresolvedPlace {
  place: string;
  full_name?: string;
  seconds: number;
  ambiguous?: boolean;
}

export interface SessionAttributionT {
  allocations: SessionAllocation[];
  unresolved: UnresolvedPlace[];
}

export interface Cluster {
  from: string;
  to: string;
  repos: string[];
  /** Bare directory names from runs with no git remote — a weaker answer than
   *  a repository, which is why it is a separate field. */
  dirs?: string[];
  commits: Commit[];
  agent_runs?: AgentRun[];
  suggested_project_id?: string;
  suggested_note?: string;
}

export interface GitHubStatus {
  connected: boolean;
  login?: string;
  last_scan_at?: string;
  last_error?: string;
  extra_emails?: string[];
}

export interface ProjectTotal {
  project_id: string;
  name: string;
  client?: string;
  /** Palette name, absent when the project has none set. */
  color?: string;
  seconds: number;
  amount_cents: number | null;
  currency: string;
  billable: boolean;
}

export interface Account {
  id: string;
  email: string;
  display_name: string;
  avatar_url?: string;
  timezone: string;
  email_verified: boolean;
  two_factor_enabled: boolean;
  created_at: string;
}

export interface AuthSession {
  id: string;
  created_at: string;
  last_seen_at: string;
  ip?: string;
  user_agent?: string;
  current: boolean;
}

export interface Repo {
  id: string;
  project_id: string;
  full_name: string;
}

const csrf = () =>
  document.cookie
    .split("; ")
    .find((c) => c.startsWith("punchcard_csrf="))
    ?.split("=")[1] ?? "";

async function call<T>(path: string, init?: { method?: string; body?: unknown }): Promise<T> {
  const method = init?.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  // The CSRF token rides on every mutation, not every body. The server guards
  // all unsafe methods, and DELETE has no body — tying the header to the body
  // made every delete in this client answer 403 until someone pressed one.
  if (method !== "GET") headers["X-CSRF-Token"] = csrf();
  if (init?.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  const response = await fetch(path, {
    method,
    headers,
    credentials: "same-origin",
    body: init?.body === undefined ? undefined : JSON.stringify(init.body),
  });

  if (response.status === 401) throw new NotSignedIn();
  if (response.status === 204) return undefined as T;

  const text = await response.text();
  const body = text ? JSON.parse(text) : null;
  if (!response.ok) {
    throw new APIError(body?.detail ?? response.statusText, response.status, body?.code);
  }
  return body as T;
}

const range = (from: Date, to: Date) =>
  `from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`;

export const api = {
  projects: (includeArchived = false) =>
    call<{ projects: Project[] }>(`/v1/projects${includeArchived ? "?include_archived=true" : ""}`)
      .then((r) => r.projects),

  createProject: (body: {
    name: string;
    client?: string;
    hourly_rate_cents?: number;
    currency?: string;
  }) => call<Project>("/v1/projects", { method: "POST", body }),

  updateProject: (id: string, body: Record<string, unknown>) =>
    call<Project>(`/v1/projects/${id}`, { method: "PATCH", body }),

  deleteProject: (id: string) =>
    call<{ archived: boolean }>(`/v1/projects/${id}`, { method: "DELETE" }),

  repos: (projectID: string) =>
    call<{ repos: Repo[] }>(`/v1/projects/${projectID}/repos`).then((r) => r.repos),

  linkRepo: (projectID: string, full_name: string) =>
    call<Repo>(`/v1/projects/${projectID}/repos`, { method: "POST", body: { full_name } }),

  unlinkRepo: (projectID: string, repoID: string) =>
    call<void>(`/v1/projects/${projectID}/repos/${repoID}`, { method: "DELETE" }),

  sessions: (from: Date, to: Date) =>
    call<{ sessions: Session[] }>(`/v1/sessions?${range(from, to)}`).then((r) => r.sessions),

  /** The running session, or null. The server answers 404 for "nothing
   *  running", which is a state rather than a failure — translated here so no
   *  call site has to catch it. */
  current: async (): Promise<Session | null> => {
    try {
      return await call<Session>("/v1/sessions/current");
    } catch (e) {
      if (e instanceof APIError && e.status === 404) return null;
      throw e;
    }
  },

  start: (project_id: string, note: string) =>
    call<Session>("/v1/sessions", { method: "POST", body: { project_id, note } }),

  stop: (id: string) => call<Session>(`/v1/sessions/${id}/stop`, { method: "POST", body: {} }),

  updateSession: (id: string, body: Record<string, unknown>) =>
    call<Session>(`/v1/sessions/${id}`, { method: "PATCH", body }),

  deleteSession: (id: string) => call<void>(`/v1/sessions/${id}`, { method: "DELETE" }),

  commits: (sessionID: string) =>
    call<{ commits: Commit[] }>(`/v1/sessions/${sessionID}/commits`).then((r) => r.commits),

  /** Every run in a window, matched or not — the day strip's second band. */
  agentRunsBetween: (from: Date, to: Date) =>
    call<{ agent_runs: AgentRun[] }>(`/v1/agent-runs?${range(from, to)}`).then(
      (r) => r.agent_runs ?? [],
    ),

  /** How a session's time divides across projects, by its evidence. */
  sessionAttribution: (sessionID: string) =>
    call<SessionAttributionT>(`/v1/sessions/${sessionID}/attribution`).then((r) => ({
      allocations: r.allocations ?? [],
      unresolved: r.unresolved ?? [],
    })),

  agentRuns: (sessionID: string) =>
    call<{ agent_runs: AgentRun[] }>(`/v1/sessions/${sessionID}/agent-runs`).then(
      (r) => r.agent_runs ?? [],
    ),

  unmatched: (from: Date, to: Date) =>
    call<{ clusters: Cluster[] }>(`/v1/github/unmatched?${range(from, to)}`)
      .then((r) => r.clusters ?? []),

  recover: (body: { project_id: string; from: string; to: string; note: string }) =>
    call<Session>("/v1/github/unmatched/recover", { method: "POST", body }),

  me: () => call<Account>("/v1/me"),

  updateMe: (body: Record<string, unknown>) =>
    call<Account>("/v1/me", { method: "PATCH", body }),

  deleteAccount: () => call<unknown>("/v1/me", { method: "DELETE" }),

  authSessions: () =>
    call<{ sessions: AuthSession[] }>("/v1/me/sessions").then((r) => r.sessions),

  revokeAuthSession: (id: string) => call<void>(`/v1/me/sessions/${id}`, { method: "DELETE" }),

  github: () => call<GitHubStatus>("/v1/github/status"),

  disconnectGitHub: () => call<unknown>("/v1/github/connection", { method: "DELETE" }),

  /** Ends the browser session server-side; the cookie is cleared in the
   *  response. The caller reloads — stale state after a sign-out is worse than
   *  a page load. */
  logout: () => call<unknown>("/v1/auth/logout", { method: "POST", body: {} }),

  /** Per-project totals. The list is normalised to an array here so no screen
   *  further in has to defend against a range with nothing in it. */
  summary: (from: Date, to: Date, attribution: "declared" | "evidence" = "declared") =>
    call<{ projects?: ProjectTotal[]; timezone: string }>(
      `/v1/reports/summary?${range(from, to)}&group_by=project&attribution=${attribution}`,
    ).then((r) => ({ ...r, projects: r.projects ?? [] })),

  /** Per-day totals, bucketed in the account's timezone by the server. */
  summaryDays: (from: Date, to: Date) =>
    call<{ days: { date: string; seconds: number }[] }>(
      `/v1/reports/summary?${range(from, to)}&group_by=day`,
    ).then((r) => r.days ?? []),
};

/** Where the browser goes to sign in.
 *
 *  One authorization does both jobs: it signs the person in, and it grants the
 *  scanner the read access it needs. Asking twice for the same provider would
 *  be worse than asking once for a little more. */
export const signInURL = "/v1/auth/oauth/github?scope=repo";

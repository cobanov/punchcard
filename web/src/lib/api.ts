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

export interface Cluster {
  from: string;
  to: string;
  repos: string[];
  commits: Commit[];
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
  seconds: number;
  amount_cents: number | null;
  currency: string;
  billable: boolean;
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
  if (init?.body !== undefined) {
    headers["Content-Type"] = "application/json";
    headers["X-CSRF-Token"] = csrf();
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

  start: (project_id: string, note: string) =>
    call<Session>("/v1/sessions", { method: "POST", body: { project_id, note } }),

  stop: (id: string) => call<Session>(`/v1/sessions/${id}/stop`, { method: "POST", body: {} }),

  updateSession: (id: string, body: Record<string, unknown>) =>
    call<Session>(`/v1/sessions/${id}`, { method: "PATCH", body }),

  deleteSession: (id: string) => call<void>(`/v1/sessions/${id}`, { method: "DELETE" }),

  commits: (sessionID: string) =>
    call<{ commits: Commit[] }>(`/v1/sessions/${sessionID}/commits`).then((r) => r.commits),

  unmatched: (from: Date, to: Date) =>
    call<{ clusters: Cluster[] }>(`/v1/github/unmatched?${range(from, to)}`)
      .then((r) => r.clusters ?? []),

  recover: (body: { project_id: string; from: string; to: string; note: string }) =>
    call<Session>("/v1/github/unmatched/recover", { method: "POST", body }),

  github: () => call<GitHubStatus>("/v1/github/status"),

  summary: (from: Date, to: Date) =>
    call<{ projects: ProjectTotal[]; timezone: string }>(
      `/v1/reports/summary?${range(from, to)}&group_by=project`,
    ),
};

/** Where the browser goes to sign in.
 *
 *  One authorization does both jobs: it signs the person in, and it grants the
 *  scanner the read access it needs. Asking twice for the same provider would
 *  be worse than asking once for a little more. */
export const signInURL = "/v1/auth/oauth/github?scope=repo";

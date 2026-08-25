import { useEffect, useRef, useState } from "react";
import { api, type Account, type AuthSession, type GitHubStatus } from "../lib/api";
import { Avatar } from "./UserMenu";

/**
 * Account settings.
 *
 * Grouped rather than listed flat — profile, reporting, connections, sessions,
 * danger — because a single column of unrelated fields makes the reader sort
 * them. Saving is explicit and the button stays disabled until something is
 * actually different, so pressing it always means something happened; the
 * confirmation is inline and fades, rather than a toast that lands somewhere
 * else on the screen.
 *
 * Destructive things sit at the bottom behind their own heading and a typed
 * confirmation. Deleting an account cannot be one click away from changing a
 * display name.
 */

export function Settings({
  account,
  github,
  onSaved,
}: {
  account: Account;
  github: GitHubStatus | null;
  onSaved: () => void;
}) {
  const [name, setName] = useState(account.display_name ?? "");
  const [tz, setTz] = useState(account.timezone);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);

  const dirty = name !== (account.display_name ?? "") || tz !== account.timezone;

  // The confirmation is a moment, not a state. Leaving "Saved" on screen makes
  // the next unsaved change look saved.
  useEffect(() => {
    if (!saved) return;
    const id = setTimeout(() => setSaved(false), 2400);
    return () => clearTimeout(id);
  }, [saved]);

  const save = async (body: Record<string, unknown>) => {
    setSaving(true);
    try {
      await api.updateMe(body);
      setError(null);
      setSaved(true);
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-6">
      {error && (
        <p className="rounded-md border-l-2 border-punch bg-card px-3 py-2 text-dim">{error}</p>
      )}

      <Group title="Profile">
        <Row label="Photo">
          <div className="flex items-center gap-3">
            <Avatar account={account} size={36} />
            <button onClick={() => fileInput.current?.click()} className="btn-ghost">
              {account.avatar_url ? "Change" : "Upload"}
            </button>
            {account.avatar_url && (
              <button onClick={() => save({ avatar_url: "" })} className="btn-bare t-caption">
                Remove
              </button>
            )}
            <input
              ref={fileInput}
              type="file"
              accept="image/png,image/jpeg,image/webp"
              className="hidden"
              onChange={async (e) => {
                const file = e.target.files?.[0];
                e.target.value = "";
                if (!file) return;
                // The API stores the photo as a data URL, so the file never
                // needs an object store — and the cap is checked here so a
                // large image fails with a sentence instead of a 422.
                if (file.size > 400_000) {
                  setError("That image is over 400 KB. Pick a smaller one.");
                  return;
                }
                const reader = new FileReader();
                reader.onload = () => void save({ avatar_url: String(reader.result) });
                reader.readAsDataURL(file);
              }}
            />
          </div>
        </Row>
        <Row label="Display name" hint="Shown in the menu and on your sessions.">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={account.email.split("@")[0]}
            className="field w-full max-w-xs py-1.5"
          />
        </Row>
        <Row label="Email">
          <p className="text-dim">
            {account.email}
            {!account.email_verified && (
              <span className="ml-2 rounded border border-line px-1.5 py-0.5 t-caption text-faint">
                unverified
              </span>
            )}
          </p>
        </Row>
      </Group>

      <Group title="Reporting">
        <Row
          label="Timezone"
          hint="Report days are cut in this zone. A session crossing midnight lands on the right local day."
        >
          <select
            value={tz}
            onChange={(e) => setTz(e.target.value)}
            className="select w-full max-w-xs py-1.5"
          >
            {zones(tz).map((z) => (
              <option key={z} value={z}>
                {z}
              </option>
            ))}
          </select>
        </Row>
      </Group>

      <div className="flex items-center gap-3">
        <button
          onClick={() => save({ display_name: name, timezone: tz })}
          disabled={!dirty || saving}
          className="btn-primary"
        >
          {saving ? "Saving…" : "Save changes"}
        </button>
        {saved && <span className="t-caption text-punch">Saved</span>}
      </div>

      <Group title="Connections">
        <Row label="GitHub" hint="Read access to the commits behind your sessions.">
          {github?.connected ? (
            <div className="flex flex-wrap items-center gap-3">
              <span className="font-mono text-dim">@{github.login}</span>
              {github.last_error && <span className="text-punch">{github.last_error}</span>}
              <button
                onClick={() =>
                  confirm("Disconnect GitHub? Commits will stop being attached to new sessions.") &&
                  void api.disconnectGitHub().then(onSaved)
                }
                className="btn-bare t-caption"
              >
                Disconnect
              </button>
            </div>
          ) : (
            <a href="/v1/auth/oauth/github?scope=repo" className="btn-ghost">
              Connect GitHub
            </a>
          )}
        </Row>
      </Group>

      <SessionsGroup />

      <Group title="Danger zone">
        <Row label="Delete account" hint="Removes every project, session and commit. Not reversible.">
          <DeleteAccount email={account.email} />
        </Row>
      </Group>
    </div>
  );
}

function Group({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section>
      <h2 className="eyebrow mb-2">{title}</h2>
      <div className="panel divide-y divide-line">{children}</div>
    </section>
  );
}

function Row({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-2 px-3 py-3 sm:flex-row sm:items-start sm:gap-6">
      <div className="w-40 shrink-0">
        <p className="font-medium">{label}</p>
        {hint && <p className="t-caption text-faint">{hint}</p>}
      </div>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

/** Where the account is signed in. Revoking a session is the one control here
 *  that matters after a laptop goes missing, so it is not buried. */
function SessionsGroup() {
  const [sessions, setSessions] = useState<AuthSession[] | null>(null);

  // Returns void, not the promise: useEffect reads a returned value as its
  // cleanup function, and a promise there is a silent bug.
  const reload = () => {
    void api
      .authSessions()
      .then(setSessions)
      .catch(() => setSessions([]));
  };
  useEffect(reload, []);

  return (
    <Group title="Signed in">
      {sessions === null ? (
        <div className="space-y-2 p-3">
          <div className="skeleton h-4 w-2/3" />
          <div className="skeleton h-4 w-1/2" />
        </div>
      ) : (
        sessions.map((s) => (
          <div key={s.id} className="flex items-center gap-3 px-3 py-2">
            <span className="min-w-0 flex-1 truncate text-dim">
              {s.user_agent || "Unknown client"}
              {s.ip && <span className="t-caption text-faint"> · {s.ip}</span>}
            </span>
            {s.current ? (
              <span className="t-caption text-faint">this device</span>
            ) : (
              <button
                onClick={() => void api.revokeAuthSession(s.id).then(reload)}
                className="btn-bare t-caption"
              >
                Revoke
              </button>
            )}
          </div>
        ))
      )}
    </Group>
  );
}

/** Deletion asks for the account's email to be typed. A confirm() dialog is
 *  dismissed by reflex; typing the address cannot be. */
function DeleteAccount({ email }: { email: string }) {
  const [typed, setTyped] = useState("");
  const [arming, setArming] = useState(false);

  if (!arming) {
    return (
      <button onClick={() => setArming(true)} className="btn-ghost hover:border-punch hover:text-punch">
        Delete account
      </button>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <input
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
        placeholder={email}
        aria-label={`Type ${email} to confirm`}
        className="field w-full max-w-xs py-1.5"
      />
      <button
        onClick={() =>
          void api.deleteAccount().then(() => {
            window.location.href = "/";
          })
        }
        disabled={typed !== email}
        className="btn-primary bg-punch text-ink"
      >
        Delete permanently
      </button>
      <button onClick={() => setArming(false)} className="btn-bare t-caption">
        Cancel
      </button>
    </div>
  );
}

/**
 * The timezone list, from the browser rather than a bundled table.
 *
 * `Intl.supportedValuesOf` is the platform's own tzdata; shipping a copy would
 * be a second list to go stale. The account's current zone is merged in even if
 * this browser does not know it, so a value set elsewhere is never silently
 * dropped by the select.
 */
function zones(current: string): string[] {
  let all: string[] = [];
  try {
    all = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] })
      .supportedValuesOf?.("timeZone") ?? [];
  } catch {
    all = [];
  }
  if (!all.length) all = ["UTC", "Europe/Istanbul", "Europe/London", "America/New_York"];
  return all.includes(current) ? all : [current, ...all];
}

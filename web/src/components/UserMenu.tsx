import { useEffect, useRef, useState } from "react";
import type { Account } from "../lib/api";

/**
 * The account menu behind the avatar.
 *
 * Opens on click rather than hover: a hover menu in the corner of the screen
 * opens itself while someone is reaching past it for something else, and the
 * two things behind it — settings and signing out — are not worth that.
 * Escape and an outside click close it, focus is visible, and the trigger says
 * what it controls.
 */

export function UserMenu({
  account,
  onSettings,
  onSignOut,
}: {
  account: Account | null;
  onSettings: () => void;
  onSignOut: () => void;
}) {
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const label = account?.display_name || account?.email?.split("@")[0] || "account";

  return (
    <div ref={wrap} className="relative">
      <button
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Account menu"
        className="flex items-center gap-2 rounded-md px-1.5 py-1 transition-colors hover:bg-raise"
      >
        <Avatar account={account} />
        <span className="t-caption hidden max-w-28 truncate text-dim sm:block">{label}</span>
      </button>

      {open && (
        <div
          role="menu"
          className="panel absolute right-0 z-20 mt-1 w-56 overflow-hidden py-1"
        >
          <div className="border-b border-line px-3 pb-2 pt-1">
            <p className="truncate font-medium">{label}</p>
            <p className="t-caption truncate text-faint">{account?.email}</p>
          </div>
          <button
            role="menuitem"
            onClick={() => {
              setOpen(false);
              onSettings();
            }}
            className="block w-full px-3 py-1.5 text-left text-dim transition-colors hover:bg-raise hover:text-text"
          >
            Settings
          </button>
          <a
            role="menuitem"
            href="/v1/me/export"
            className="block px-3 py-1.5 text-dim transition-colors hover:bg-raise hover:text-text"
          >
            Download my data
          </a>
          <button
            role="menuitem"
            onClick={() => {
              setOpen(false);
              onSignOut();
            }}
            className="block w-full border-t border-line px-3 py-1.5 text-left text-dim transition-colors hover:bg-raise hover:text-text"
          >
            Sign out
          </button>
        </div>
      )}
    </div>
  );
}

/**
 * The avatar, with initials as the fallback.
 *
 * An account with no photo still gets a mark rather than a grey circle: a
 * placeholder that says nothing is a placeholder that looks broken. Initials
 * come from the display name, or the email's local part when there is no name.
 */
export function Avatar({ account, size = 22 }: { account: Account | null; size?: number }) {
  const source = account?.display_name || account?.email?.split("@")[0] || "?";
  const initials = source
    .split(/[\s._-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]!.toUpperCase())
    .join("");

  if (account?.avatar_url) {
    return (
      <img
        src={account.avatar_url}
        alt=""
        width={size}
        height={size}
        className="shrink-0 rounded-full object-cover"
        style={{ width: size, height: size }}
      />
    );
  }
  return (
    <span
      aria-hidden
      className="flex shrink-0 items-center justify-center rounded-full bg-raise font-medium text-dim"
      style={{ width: size, height: size, fontSize: size * 0.42 }}
    >
      {initials || "?"}
    </span>
  );
}

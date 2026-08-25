/** How punchcard renders time.
 *
 *  Three formats for three questions. The running clock is watched, so it ticks
 *  in seconds. A total is read, so it is said the way a person says it. A time
 *  of day is scanned, so it is fixed-width. */

const pad = (n: number) => String(n).padStart(2, "0");

/** The running timer: `01:42:07`, ticking.
 *
 *  Hours are never wrapped at 24. A timer left running over a weekend should
 *  read `61:20:00` and look wrong, because it is. */
export function clock(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  return `${pad(Math.floor(s / 3600))}:${pad(Math.floor((s % 3600) / 60))}:${pad(s % 60)}`;
}

/** A total: `6h 12m`. Nobody bills in seconds — and the landing page already
 *  speaks in h/m, so the app saying `6s 12d` had the product using two
 *  vocabularies for one number. */
export function total(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h && m) return `${h}h ${m}m`;
  if (h) return `${h}h`;
  return `${m}m`;
}

export const hhmm = (iso: string | Date) =>
  new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });

/** Elapsed seconds for a running session.
 *
 *  Derived from started_at, never from the `seconds` the server last reported —
 *  otherwise the clock freezes between refreshes and the product's one live
 *  element stops being live. */
export const elapsed = (session: { started_at: string; running: boolean; seconds: number }) =>
  session.running ? (Date.now() - new Date(session.started_at).getTime()) / 1000 : session.seconds;

/** Money, formatted. It is never computed here: every amount arrives already
 *  worked out in integer minor units. */
export const money = (cents: number, currency: string) =>
  `${(cents / 100).toLocaleString(undefined, { minimumFractionDigits: 2 })} ${currency}`;

/** Hours since midnight, as a float — the coordinate the day card is drawn in. */
export function hourOf(iso: string | Date): number {
  const d = new Date(iso);
  return d.getHours() + d.getMinutes() / 60 + d.getSeconds() / 3600;
}

export const startOfToday = () => {
  const d = new Date();
  d.setHours(0, 0, 0, 0);
  return d;
};

export const daysAgo = (n: number) => new Date(Date.now() - n * 864e5);

/** Midnight at the start of the given date's day. */
export function dayStart(d: Date): Date {
  const out = new Date(d);
  out.setHours(0, 0, 0, 0);
  return out;
}

/** Calendar-day addition — setDate, not +86400s, so DST cannot skip a day. */
export function addDays(d: Date, n: number): Date {
  const out = new Date(d);
  out.setDate(out.getDate() + n);
  return out;
}

export const isToday = (d: Date) => dayStart(d).getTime() === startOfToday().getTime();

/** "Mon 25 Aug" — the header label for a browsed day. */
export const dayName = (d: Date) =>
  d.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" });

/** The value/format a native date input speaks. Local, not UTC — toISOString
 *  would shift the day for anyone east of Greenwich. */
export function toDateInput(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

export const firstLine = (s: string) => s.split("\n")[0]?.trim() ?? "";

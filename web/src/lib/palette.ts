/**
 * The project palette.
 *
 * Colours are stored as NAMES, not hex values — the column has a CHECK
 * constraint listing exactly these eight. The name is resolved to a value here,
 * at paint time, so the palette can be retuned without touching a single row.
 *
 * The values are picked to sit on the zinc background: saturated enough to
 * separate from each other at the size of a four-pixel dot, dark enough not to
 * glare next to 13px text.
 */
export const PALETTE = {
  red: "#e5484d",
  amber: "#e9973f",
  green: "#46a758",
  teal: "#2eb6a8",
  blue: "#5b8def",
  violet: "#8b5cf6",
  pink: "#d6409f",
  slate: "#8b8b96",
} as const;

export type ColorName = keyof typeof PALETTE;
export const COLOR_NAMES = Object.keys(PALETTE) as ColorName[];

/**
 * Amber is not handed out automatically.
 *
 * It is the app's one reserved colour: running time, and the commits that prove
 * it. If the fallback could assign it, a project would eventually wear the
 * colour that is supposed to mean "this is happening now", and the signal that
 * makes the timer readable at a glance would quietly stop being a signal.
 * Choosing it deliberately is still allowed — that is the user's call, not the
 * shuffler's.
 */
const AUTO = COLOR_NAMES.filter((c) => c !== "amber");

/**
 * The colour to paint a project, whether or not anyone picked one.
 *
 * An unset project still gets a stable colour derived from its id, so a chart
 * of eight projects reads as eight projects on the first run, with nothing to
 * configure. Setting a colour overrides it. The hash is the cheap FNV-style
 * one — it only has to be stable and spread eight ways.
 */
export function projectColor(color: string | undefined, id: string): string {
  if (color && color in PALETTE) return PALETTE[color as ColorName];
  let h = 2166136261;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return PALETTE[AUTO[Math.abs(h) % AUTO.length]!];
}

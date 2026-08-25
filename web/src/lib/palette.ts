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

/** Stable FNV-style hash. It only has to be deterministic and spread evenly. */
function hash(id: string): number {
  let h = 2166136261;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return Math.abs(h);
}

/**
 * One colour per project, and no two the same if it can be helped.
 *
 * Hashing an id straight into the palette is stable but collides: seven
 * automatic colours and four projects collide about half the time, and two
 * projects sharing a colour is precisely the thing colour was added to fix.
 * So the hash picks a starting point and the assignment probes forward from
 * there for one nobody has taken.
 *
 * Colours somebody chose are claimed first, so an automatic project never
 * steals a colour that a deliberate one is already wearing. Order is by id, not
 * by whatever order the caller happened to fetch in, so the same set of
 * projects always comes out the same way. Past eight projects the palette runs
 * out and repeats begin — at that point a repeat is honest, because there are
 * more projects than there are distinguishable colours.
 */
export function assignColors(items: { id: string; color?: string }[]): Map<string, string> {
  const out = new Map<string, string>();
  const taken = new Set<string>();

  for (const it of items) {
    if (it.color && it.color in PALETTE) {
      out.set(it.id, PALETTE[it.color as ColorName]);
      taken.add(it.color);
    }
  }

  const auto = items.filter((it) => !out.has(it.id)).sort((a, b) => (a.id < b.id ? -1 : 1));
  for (const it of auto) {
    const start = hash(it.id) % AUTO.length;
    let name = AUTO[start]!;
    for (let step = 0; step < AUTO.length; step++) {
      const candidate = AUTO[(start + step) % AUTO.length]!;
      if (!taken.has(candidate)) {
        name = candidate;
        break;
      }
    }
    taken.add(name);
    out.set(it.id, PALETTE[name]);
  }
  return out;
}

/**
 * The colour for one project on its own, with no set to compare against.
 *
 * Used where there is genuinely only one — the swatch that previews what
 * "automatic" would give this project. Lists should use assignColors instead,
 * which can also keep them apart from each other.
 */
export function projectColor(color: string | undefined, id: string): string {
  if (color && color in PALETTE) return PALETTE[color as ColorName];
  return PALETTE[AUTO[hash(id) % AUTO.length]!];
}

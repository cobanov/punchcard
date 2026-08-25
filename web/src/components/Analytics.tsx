import { useCallback, useEffect, useState } from "react";
import { api, type ProjectTotal } from "../lib/api";
import { money, total } from "../lib/format";
import { assignColors } from "../lib/palette";

/**
 * Analytics: what happened, and whether it is more or less than before.
 *
 * Three questions in one screen. How much time — the headline. Compared to
 * what — every headline carries its change against the immediately preceding
 * window of the same length, which is the only comparison that does not need
 * explaining. And where did it go — the bars, then the breakdown.
 *
 * A single number with no baseline is trivia. "6h 12m" means nothing until it
 * sits next to last week's, which is why the comparison is not an optional
 * extra here but part of every metric.
 */

type RangeKey = "today" | "7d" | "30d" | "month" | "custom";

interface Range {
  from: Date;
  to: Date;
  key: RangeKey;
}

/**
 * How far ahead of UTC the zone is at that instant, in milliseconds.
 *
 * There is no API that just says this, so it is read back out of a formatted
 * date: format the instant into the zone, reassemble those wall-clock fields
 * as if they were UTC, and the difference is the offset.
 */
function zoneOffset(tz: string, at: Date): number {
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      hour12: false,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    })
      .formatToParts(at)
      .map((p) => [p.type, p.value]),
  ) as Record<string, string>;
  const asIfUTC = Date.UTC(
    Number(parts.year),
    Number(parts.month) - 1,
    Number(parts.day),
    Number(parts.hour) % 24,
    Number(parts.minute),
    Number(parts.second),
  );
  return asIfUTC - at.getTime();
}

/**
 * The instant a given calendar day begins in the account's timezone.
 *
 * Date.UTC absorbs out-of-range days, so day 0 is last month's last day and
 * day 32 is next month's first — the callers below lean on that instead of
 * doing calendar arithmetic themselves.
 *
 * Two passes, because the offset has to be sampled somewhere and the only
 * available guess is the wrong side of a daylight-saving change: the first
 * pass corrects UTC midnight into roughly the right instant, the second
 * samples the offset actually in force there.
 */
function zonedInstant(tz: string, y: number, m: number, d: number): Date {
  const utc = Date.UTC(y, m - 1, d);
  const once = utc - zoneOffset(tz, new Date(utc));
  return new Date(utc - zoneOffset(tz, new Date(once)));
}

/** Today's calendar date in the account's zone, which is rarely the browser's. */
function todayIn(tz: string): { y: number; m: number; d: number } {
  const [y, m, d] = new Intl.DateTimeFormat("en-CA", {
    timeZone: tz,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  })
    .format(new Date())
    .split("-")
    .map(Number);
  return { y: y!, m: m!, d: d! };
}

/**
 * Ranges are cut in the ACCOUNT's timezone, not the browser's.
 *
 * These two are the same zone often enough that using the browser's looked
 * correct for a long time. They were not the same here: the account cut its
 * days in UTC while the browser sat three hours ahead, so "the last 7 days"
 * began at 21:00 on the eighth day back — and both the server's grouping and
 * the chart dutifully reported eight days for a seven-day preset.
 *
 * A range and the days inside it have to be cut by the same clock.
 */
function presetRange(key: Exclude<RangeKey, "custom">, tz: string): Range {
  const { y, m, d } = todayIn(tz);
  const tomorrow = zonedInstant(tz, y, m, d + 1); // exclusive end, every preset
  switch (key) {
    case "today":
      return { from: zonedInstant(tz, y, m, d), to: tomorrow, key };
    case "7d":
      return { from: zonedInstant(tz, y, m, d - 6), to: tomorrow, key };
    case "30d":
      return { from: zonedInstant(tz, y, m, d - 29), to: tomorrow, key };
    case "month":
      return { from: zonedInstant(tz, y, m, 1), to: tomorrow, key };
  }
}

interface Data {
  projects: ProjectTotal[];
  days: { date: string; seconds: number }[];
  previousSeconds: number;
  timezone: string;
  fetchedAt: Date;
}

export function Analytics({ timezone }: { timezone: string }) {
  const [range, setRange] = useState<Range>(() => presetRange("7d", timezone));
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState<string | null>(null);

  // The account loads after the first paint, so the opening range is cut in a
  // placeholder zone. Re-cut it when the real one arrives — but never a custom
  // range, where the dates are the user's own choice and not ours to move.
  useEffect(() => {
    setRange((r) => (r.key === "custom" ? r : presetRange(r.key, timezone)));
  }, [timezone]);

  // "evidence" is the default here even though the API defaults to
  // "declared": the API keeps old clients' numbers stable, the app shows the
  // truthful split. The switch exists for anyone who wants the timer's story.
  const [mode, setMode] = useState<"evidence" | "declared">("evidence");

  const load = useCallback(async (r: Range, m: "evidence" | "declared") => {
    setData(null);
    setError(null);
    try {
      // The previous window is the same length, ending where this one starts —
      // "last 7 days vs the 7 before" needs no caption to be understood.
      const span = r.to.getTime() - r.from.getTime();
      const prevFrom = new Date(r.from.getTime() - span);

      const [summary, days, previous] = await Promise.all([
        api.summary(r.from, r.to, m),
        api.summaryDays(r.from, r.to),
        api.summary(prevFrom, r.from, m),
      ]);
      setData({
        projects: summary.projects,
        days: fillCalendar(days, r, summary.timezone),
        previousSeconds: previous.projects.reduce((sum, p) => sum + p.seconds, 0),
        timezone: summary.timezone,
        fetchedAt: new Date(),
      });
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    void load(range, mode);
  }, [range, mode, load]);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <RangePicker timezone={timezone} range={range} onChange={setRange} />
        <div
          className="flex gap-0.5 rounded-lg border border-line bg-card p-0.5"
          role="group"
          aria-label="Attribution"
        >
          {(["evidence", "declared"] as const).map((m) => (
            <button
              key={m}
              onClick={() => setMode(m)}
              aria-pressed={mode === m}
              className={
                mode === m
                  ? "rounded-md bg-raise px-2.5 py-1 text-text"
                  : "rounded-md px-2.5 py-1 text-dim transition-colors hover:text-text"
              }
            >
              {m === "evidence" ? "By evidence" : "As declared"}
            </button>
          ))}
        </div>
      </div>

      {error && (
        <p className="rounded-md border-l-2 border-punch bg-card px-3 py-2 text-dim">{error}</p>
      )}

      {data === null ? (
        <Skeleton />
      ) : (
        <Loaded data={data} range={range} mode={mode} onRefresh={() => void load(range, mode)} />
      )}
    </div>
  );
}

function Loaded({
  data,
  range,
  mode,
  onRefresh,
}: {
  data: Data;
  range: Range;
  mode: "evidence" | "declared";
  onRefresh: () => void;
}) {
  const seconds = data.projects.reduce((sum, p) => sum + p.seconds, 0);
  const billable = data.projects.reduce(
    (sum, p) => sum + (p.amount_cents ?? 0),
    0,
  );
  const currency = data.projects.find((p) => p.amount_cents != null)?.currency ?? "";
  const activeDays = data.days.filter((d) => d.seconds > 0).length;

  if (seconds === 0) {
    return (
      <div className="panel px-4 py-10 text-center">
        <p className="text-dim">No time recorded in this period.</p>
        <p className="t-caption mt-1 text-faint">
          Try a wider range — {range.key === "today" ? "the last 7 days" : "the last 30 days"} —
          or start a timer and come back.
        </p>
      </div>
    );
  }

  return (
    <>
      <div className="grid gap-3 sm:grid-cols-3">
        <Metric
          label="Tracked"
          value={total(seconds)}
          change={change(seconds, data.previousSeconds)}
          compare={`vs ${total(data.previousSeconds)} before`}
        />
        <Metric
          label="Billable"
          value={billable ? money(billable, currency) : "—"}
          hint={billable ? undefined : "No project in this range has a rate"}
        />
        <Metric
          label="Days worked"
          value={String(activeDays)}
          hint={`of ${data.days.length} in range`}
        />
      </div>

      <DayChart days={data.days} timezone={data.timezone} />

      <Breakdown projects={data.projects} totalSeconds={seconds} />

      <div className="flex items-center gap-3 px-1">
        <span className="t-caption text-faint">
          Updated {data.fetchedAt.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })} ·
          days cut in {data.timezone}
        </span>
        {/* A number whose method is a secret is the thing this whole feature
            exists to end, so the method is on screen next to the number. */}
        <span className="t-caption text-faint">
          ·{" "}
          {mode === "evidence"
            ? "each minute goes to the project whose evidence was active; shared minutes split evenly; quiet minutes follow the timer"
            : "every minute follows the timer's declared project"}
        </span>
        <button onClick={onRefresh} className="btn-bare t-caption">
          Refresh
        </button>
        <a
          href={`/v1/reports/export.csv?from=${encodeURIComponent(range.from.toISOString())}&to=${encodeURIComponent(range.to.toISOString())}&attribution=${mode}`}
          className="btn-bare t-caption ml-auto"
        >
          Download CSV
        </a>
      </div>
    </>
  );
}

/** Percentage for large numbers, the absolute difference for small ones — a
 *  jump from 12m to 30m reads better as "+18m" than as "+150%". */
function change(now: number, before: number): { text: string; up: boolean } | null {
  if (!before) return null;
  const delta = now - before;
  if (delta === 0) return null;
  const up = delta > 0;
  const text =
    before < 3600
      ? `${up ? "+" : "−"}${total(Math.abs(delta))}`
      : `${up ? "+" : "−"}${Math.round((Math.abs(delta) / before) * 100)}%`;
  return { text, up };
}

function Metric({
  label,
  value,
  change,
  compare,
  hint,
}: {
  label: string;
  value: string;
  change?: { text: string; up: boolean } | null;
  compare?: string;
  hint?: string;
}) {
  return (
    <div className="panel px-3 py-3">
      <p className="eyebrow">{label}</p>
      <p className="t-metric mt-1 tnum">{value}</p>
      <p className="t-caption mt-1 text-faint">
        {change && (
          // Direction is carried by the arrow and the wording, not only by
          // colour — the change has to be readable without seeing colour.
          <span className={change.up ? "text-punch" : "text-dim"}>
            {change.up ? "↑" : "↓"} {change.text}{" "}
          </span>
        )}
        {compare ?? hint ?? ""}
      </p>
    </div>
  );
}

/**
 * Time per day.
 *
 * A bar per day with a labelled y-axis and readable ticks — a chart whose axis
 * is implied is a chart you have to guess at. Bars carry their exact value in
 * the title and in the accessible name, so the information is not locked in the
 * pixels.
 */
function DayChart({ days, timezone }: { days: { date: string; seconds: number }[]; timezone: string }) {
  if (days.length < 2) return null;
  const peak = Math.max(...days.map((d) => d.seconds), 1);
  // Ticks at sensible hour marks rather than at the raw maximum.
  const topHours = Math.max(1, Math.ceil(peak / 3600));
  const ceiling = topHours * 3600;

  return (
    <figure className="panel px-3 py-3">
      <figcaption className="eyebrow mb-3">
        Hours per day <span className="normal-case tracking-normal">({timezone})</span>
      </figcaption>
      <div className="flex gap-2">
        {/* y-axis */}
        <div className="flex w-8 shrink-0 flex-col justify-between text-right">
          {[topHours, topHours / 2, 0].map((h) => (
            <span key={h} className="t-caption tnum font-mono text-faint">
              {h % 1 === 0 ? h : h.toFixed(1)}h
            </span>
          ))}
        </div>
        <div className="min-w-0 flex-1">
          <div className="relative flex h-28 items-end gap-[3px] border-b border-line">
            {/* Gridlines, behind the bars, quiet enough to read past. */}
            <span className="absolute inset-x-0 top-0 h-px bg-line/60" aria-hidden />
            <span className="absolute inset-x-0 top-1/2 h-px bg-line/60" aria-hidden />
            {days.map((d, i) => {
              const pct = (d.seconds / ceiling) * 100;
              return (
                <div
                  key={d.date}
                  title={`${d.date} · ${total(d.seconds)}`}
                  aria-label={`${d.date}: ${total(d.seconds)}`}
                  className="group relative flex min-w-0 flex-1 items-end"
                  style={{ height: "100%" }}
                >
                  <div
                    className={
                      d.seconds > 0
                        ? "bar-grow w-full rounded-t-sm bg-punch/80 transition-colors group-hover:bg-punch"
                        : "bar-grow w-full rounded-t-sm bg-raise"
                    }
                    // The columns arrive left to right, which is the direction
                    // the axis is read in. Capped, because a thirty-day range
                    // should not spend a second dealing itself out.
                    style={{
                      height: d.seconds > 0 ? `${Math.max(pct, 2)}%` : "2px",
                      animationDelay: `${Math.min(i * 24, 240)}ms`,
                    }}
                  />
                </div>
              );
            })}
          </div>
          {/* x-axis: first, middle and last, so labels never collide. */}
          <div className="mt-1 flex justify-between">
            {[days[0], days[Math.floor(days.length / 2)], days[days.length - 1]]
              .filter(Boolean)
              .map((d, i) => (
                <span key={i} className="t-caption font-mono text-faint">
                  {shortDate(d!.date)}
                </span>
              ))}
          </div>
        </div>
      </div>
    </figure>
  );
}

/**
 * Where the time went, by project.
 *
 * The only segmentation punchcard has that means anything — it has no channels
 * or devices to slice by, and inventing a dimension to fill a section would be
 * worse than one honest one.
 */
function Breakdown({
  projects,
  totalSeconds,
}: {
  projects: ProjectTotal[];
  totalSeconds: number;
}) {
  const colors = assignColors(projects.map((p) => ({ id: p.project_id, color: p.color })));
  return (
    // Same template for the header and every row, so the share, the time and
    // the amount each land on one vertical edge instead of near it.
    <section className="panel tbl-breakdown overflow-hidden">
      <div className="tbl-head hidden mid:grid">
        <span className="c-project">Project</span>
        <span className="c-client">Client</span>
        <span className="c-share text-right">Share</span>
        <span className="c-time text-right">Time</span>
        <span className="c-amount text-right">Amount</span>
      </div>
      <ul className="divide-y divide-line">
        {projects.map((p) => {
          const share = Math.round((p.seconds / totalSeconds) * 100);
          return (
            <li key={p.project_id} className="pb-2 pt-1">
              <div className="tbl-row">
                <span className="c-project flex min-w-0 items-center gap-2">
                  <span
                    className="size-2 shrink-0 rounded-full"
                    style={{ background: colors.get(p.project_id) }}
                    aria-hidden
                  />
                  <span className="truncate font-medium">{p.name}</span>
                </span>
                <span className="c-client truncate text-dim">{p.client || "—"}</span>
                <span className="c-share tbl-num t-caption text-faint">{share}%</span>
                <span className="c-time tbl-num text-dim">{total(p.seconds)}</span>
                <span className="c-amount tbl-num">
                  {p.amount_cents == null ? (
                    <span className="text-faint">—</span>
                  ) : (
                    money(p.amount_cents, p.currency)
                  )}
                </span>
              </div>
              {/* The bar wears the project's own colour, not the app's amber.
                  Amber means running time and the commits that prove it; a
                  static share of a finished range is neither. */}
              <div className="mx-3 h-1 overflow-hidden rounded-full bg-raise" aria-hidden>
                <div
                  className="h-full rounded-full"
                  style={{
                    width: `${share}%`,
                    background: colors.get(p.project_id),
                    opacity: 0.8,
                  }}
                />
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

/**
 * The range picker: shortcuts first, custom behind them.
 *
 * Five presets cover almost every visit, and the two date inputs are typed
 * rather than clicked through months — the native control accepts keyboard
 * entry and shows the OS calendar, which is both more familiar and less code
 * than a picker of our own.
 */
function RangePicker({
  range,
  onChange,
  timezone,
}: {
  range: Range;
  onChange: (r: Range) => void;
  timezone: string;
}) {
  const [custom, setCustom] = useState(range.key === "custom");

  const presets: { key: Exclude<RangeKey, "custom">; label: string }[] = [
    { key: "today", label: "Today" },
    { key: "7d", label: "7 days" },
    { key: "30d", label: "30 days" },
    { key: "month", label: "This month" },
  ];

  return (
    <div className="flex flex-wrap items-center gap-2">
      <div className="flex items-center gap-0.5 rounded-lg border border-line bg-card p-0.5">
        {presets.map((p) => (
          <button
            key={p.key}
            onClick={() => {
              setCustom(false);
              onChange(presetRange(p.key, timezone));
            }}
            aria-pressed={range.key === p.key}
            className={
              range.key === p.key
                ? "rounded-md bg-raise px-2.5 py-1 text-text"
                : "rounded-md px-2.5 py-1 text-dim transition-colors hover:text-text"
            }
          >
            {p.label}
          </button>
        ))}
        <button
          onClick={() => setCustom((c) => !c)}
          aria-pressed={range.key === "custom"}
          className={
            range.key === "custom"
              ? "rounded-md bg-raise px-2.5 py-1 text-text"
              : "rounded-md px-2.5 py-1 text-dim transition-colors hover:text-text"
          }
        >
          Custom
        </button>
      </div>

      {custom && (
        <div className="flex items-center gap-1.5">
          <input
            type="date"
            value={dayKey(range.from, timezone)}
            max={dayKey(new Date(), timezone)}
            aria-label="From"
            onChange={(e) =>
              e.target.value &&
              onChange({ ...range, from: typedDay(e.target.value, timezone, 0), key: "custom" })
            }
            className="field py-1 font-mono t-caption"
          />
          <span className="text-faint">→</span>
          <input
            type="date"
            // range.to is the exclusive next midnight; one millisecond back is
            // the last day the range actually contains.
            value={dayKey(new Date(range.to.getTime() - 1), timezone)}
            max={dayKey(new Date(), timezone)}
            aria-label="To"
            onChange={(e) =>
              e.target.value &&
              onChange({
                ...range,
                // The input names an inclusive last day; the API wants an
                // exclusive bound, so the range ends at the next midnight — in
                // the account's zone, like every other boundary here.
                to: typedDay(e.target.value, timezone, 1),
                key: "custom",
              })
            }
            className="field py-1 font-mono t-caption"
          />
        </div>
      )}
    </div>
  );
}

function Skeleton() {
  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-3">
        {[0, 1, 2].map((i) => (
          <div key={i} className="panel px-3 py-3">
            <div className="skeleton h-3 w-16" />
            <div className="skeleton mt-2 h-6 w-24" />
            <div className="skeleton mt-2 h-3 w-28" />
          </div>
        ))}
      </div>
      <div className="panel p-3">
        <div className="skeleton h-3 w-28" />
        <div className="skeleton mt-3 h-28 w-full" />
      </div>
    </div>
  );
}

/**
 * The account's calendar day containing an instant, as YYYY-MM-DD.
 *
 * The date inputs show and accept days, and a day only means something once
 * you say whose clock drew it. Formatting these in the browser's zone put the
 * boundary an hour or three away from where the report actually cut it.
 */
function dayKey(at: Date, tz: string): string {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: tz,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(at);
}

/** A YYYY-MM-DD from a date input, read as a day in the account's zone. */
function typedDay(value: string, tz: string, plusDays: number): Date {
  const [y, m, d] = value.split("-").map(Number);
  return zonedInstant(tz, y!, m!, d! + plusDays);
}

/**
 * Turn the server's sparse answer into a calendar.
 *
 * `group_by=day` returns the days that have work in them — a quiet Tuesday is
 * simply absent. Two screens read that list as if it were the range itself,
 * and both were wrong in a way that looked plausible: "days worked" divided by
 * the rows it was counting and could only ever say "1 of 1", and the chart
 * plotted the returned rows side by side, so a week with Monday and Friday in
 * it drew two adjacent bars and hid the three empty days between them.
 *
 * A day with no work is data. It gets a row, with zero in it.
 *
 * Steps are twelve hours, not twenty-four: a daylight-saving day is 23 or 25
 * hours long, and a 24-hour step across one either skips a date or repeats it.
 * Sampling twice a day and deduping by the formatted key cannot miss one. The
 * keys are formatted in the ACCOUNT's zone, the same zone the server cut its
 * days in — formatting in the browser's zone would shift every label for
 * anyone travelling.
 */
function fillCalendar(
  rows: { date: string; seconds: number }[],
  range: Range,
  timezone: string,
): { date: string; seconds: number }[] {
  // en-CA formats as YYYY-MM-DD, which is the shape the server already sends.
  const fmt = new Intl.DateTimeFormat("en-CA", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  });
  const worked = new Map(rows.map((r) => [r.date, r.seconds]));
  const out: { date: string; seconds: number }[] = [];
  const seen = new Set<string>();
  // `to` is exclusive everywhere in this file — the presets end at the start of
  // TOMORROW — so the loop stops before it, or every range gains a day it does
  // not contain.
  for (let t = range.from.getTime(); t < range.to.getTime(); t += 12 * 3600 * 1000) {
    const key = fmt.format(new Date(t));
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({ date: key, seconds: worked.get(key) ?? 0 });
  }
  return out;
}

/** "25 Aug" from the server's YYYY-MM-DD, parsed as a local date so the label
 *  cannot slip a day. */
function shortDate(iso: string): string {
  const [y, m, d] = iso.split("-").map(Number);
  return new Date(y!, (m ?? 1) - 1, d ?? 1).toLocaleDateString([], {
    day: "numeric",
    month: "short",
  });
}

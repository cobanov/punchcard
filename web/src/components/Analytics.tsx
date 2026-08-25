import { useCallback, useEffect, useState } from "react";
import { api, type ProjectTotal } from "../lib/api";
import { addDays, dayStart, money, toDateInput, total } from "../lib/format";

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

function presetRange(key: Exclude<RangeKey, "custom">): Range {
  const now = new Date();
  const today = dayStart(now);
  switch (key) {
    case "today":
      return { from: today, to: addDays(today, 1), key };
    case "7d":
      return { from: addDays(today, -6), to: addDays(today, 1), key };
    case "30d":
      return { from: addDays(today, -29), to: addDays(today, 1), key };
    case "month":
      return { from: new Date(now.getFullYear(), now.getMonth(), 1), to: addDays(today, 1), key };
  }
}

interface Data {
  projects: ProjectTotal[];
  days: { date: string; seconds: number }[];
  previousSeconds: number;
  timezone: string;
  fetchedAt: Date;
}

export function Analytics() {
  const [range, setRange] = useState<Range>(() => presetRange("7d"));
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (r: Range) => {
    setData(null);
    setError(null);
    try {
      // The previous window is the same length, ending where this one starts —
      // "last 7 days vs the 7 before" needs no caption to be understood.
      const span = r.to.getTime() - r.from.getTime();
      const prevFrom = new Date(r.from.getTime() - span);

      const [summary, days, previous] = await Promise.all([
        api.summary(r.from, r.to),
        api.summaryDays(r.from, r.to),
        api.summary(prevFrom, r.from),
      ]);
      setData({
        projects: summary.projects,
        days,
        previousSeconds: previous.projects.reduce((sum, p) => sum + p.seconds, 0),
        timezone: summary.timezone,
        fetchedAt: new Date(),
      });
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    void load(range);
  }, [range, load]);

  return (
    <div className="space-y-4">
      <RangePicker range={range} onChange={setRange} />

      {error && (
        <p className="rounded-md border-l-2 border-punch bg-card px-3 py-2 text-dim">{error}</p>
      )}

      {data === null ? (
        <Skeleton />
      ) : (
        <Loaded data={data} range={range} onRefresh={() => void load(range)} />
      )}
    </div>
  );
}

function Loaded({
  data,
  range,
  onRefresh,
}: {
  data: Data;
  range: Range;
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
          hint={`of ${data.days.length || dayCount(range)} in range`}
        />
      </div>

      <DayChart days={data.days} timezone={data.timezone} />

      <Breakdown projects={data.projects} totalSeconds={seconds} />

      <div className="flex items-center gap-3 px-1">
        <span className="t-caption text-faint">
          Updated {data.fetchedAt.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })} ·
          days cut in {data.timezone}
        </span>
        <button onClick={onRefresh} className="btn-bare t-caption">
          Refresh
        </button>
        <a
          href={`/v1/reports/export.csv?from=${encodeURIComponent(range.from.toISOString())}&to=${encodeURIComponent(range.to.toISOString())}`}
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
            {days.map((d) => {
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
                        ? "w-full rounded-t-sm bg-punch/80 transition-colors group-hover:bg-punch"
                        : "w-full rounded-t-sm bg-raise"
                    }
                    style={{ height: d.seconds > 0 ? `${Math.max(pct, 2)}%` : "2px" }}
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
  return (
    <section className="panel overflow-hidden">
      <h3 className="eyebrow border-b border-line px-3 py-2">By project</h3>
      <ul className="divide-y divide-line">
        {projects.map((p) => {
          const share = Math.round((p.seconds / totalSeconds) * 100);
          return (
            <li key={p.project_id} className="px-3 py-2">
              <div className="flex items-baseline gap-3">
                <span className="w-40 shrink-0 truncate font-medium">{p.name}</span>
                <span className="min-w-0 flex-1 truncate text-dim">{p.client}</span>
                <span className="tnum w-10 shrink-0 text-right font-mono t-caption text-faint">
                  {share}%
                </span>
                <span className="tnum w-16 shrink-0 text-right font-mono text-dim">
                  {total(p.seconds)}
                </span>
                <span className="tnum w-24 shrink-0 text-right font-mono">
                  {p.amount_cents == null ? (
                    <span className="text-faint">—</span>
                  ) : (
                    money(p.amount_cents, p.currency)
                  )}
                </span>
              </div>
              <div className="mt-1.5 h-1 overflow-hidden rounded-full bg-raise" aria-hidden>
                <div className="h-full rounded-full bg-punch/70" style={{ width: `${share}%` }} />
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
function RangePicker({ range, onChange }: { range: Range; onChange: (r: Range) => void }) {
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
              onChange(presetRange(p.key));
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
            value={toDateInput(range.from)}
            max={toDateInput(new Date())}
            aria-label="From"
            onChange={(e) =>
              e.target.value &&
              onChange({ ...range, from: new Date(`${e.target.value}T00:00:00`), key: "custom" })
            }
            className="field py-1 font-mono t-caption"
          />
          <span className="text-faint">→</span>
          <input
            type="date"
            value={toDateInput(addDays(range.to, -1))}
            max={toDateInput(new Date())}
            aria-label="To"
            onChange={(e) =>
              e.target.value &&
              onChange({
                ...range,
                // The input names an inclusive last day; the API wants an
                // exclusive bound, so the range ends at the next midnight.
                to: addDays(new Date(`${e.target.value}T00:00:00`), 1),
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

const dayCount = (r: Range) =>
  Math.max(1, Math.round((r.to.getTime() - r.from.getTime()) / 864e5));

/** "25 Aug" from the server's YYYY-MM-DD, parsed as a local date so the label
 *  cannot slip a day. */
function shortDate(iso: string): string {
  const [y, m, d] = iso.split("-").map(Number);
  return new Date(y!, (m ?? 1) - 1, d ?? 1).toLocaleDateString([], {
    day: "numeric",
    month: "short",
  });
}

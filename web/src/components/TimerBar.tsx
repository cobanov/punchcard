import { useEffect, useRef, useState } from "react";
import type { Project, Session } from "../lib/api";
import { clock, elapsed, hhmm } from "../lib/format";

/**
 * The one control that matters.
 *
 * Everything else on the page is read; this is pressed. The whole interaction
 * budget is: type a sentence, Enter. The project defaults to the last one used,
 * so most starts never touch the select, and `n` reaches the input from
 * anywhere so the mouse is optional.
 *
 * While running, the clock is the only coloured, only large, only moving thing
 * on the page — which is the entire visual hierarchy this tool needs.
 */

interface Props {
  current: Session | null;
  projects: Project[];
  projectName: (id: string) => string;
  onStart: (projectID: string, note: string) => Promise<void>;
  onStop: () => Promise<void>;
  busy: boolean;
}

export function TimerBar({ current, projects, projectName, onStart, onStop, busy }: Props) {
  const [note, setNote] = useState("");
  const [projectID, setProjectID] = useState("");
  const [, setTick] = useState(0);
  const noteRef = useRef<HTMLInputElement>(null);

  // The clock ticks locally. A timer that only moved when the network answered
  // would look broken every time the network was slow.
  useEffect(() => {
    if (!current?.running) return;
    const id = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(id);
  }, [current?.running]);

  useEffect(() => {
    if (!projectID && projects.length) setProjectID(projects[0]!.id);
  }, [projects, projectID]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement).tagName;
      if (["INPUT", "TEXTAREA", "SELECT"].includes(tag)) {
        if (e.key === "Escape") (e.target as HTMLElement).blur();
        return;
      }
      if (e.key === "n") {
        e.preventDefault();
        noteRef.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  if (current?.running) {
    return (
      <section aria-label="Running timer" className="panel flex items-center gap-3 px-4 py-3">
        <span className="breathe h-2 w-2 shrink-0 rounded-full bg-punch" aria-hidden />
        <span className="t-clock shrink-0 text-punch">
          {clock(elapsed(current))}
        </span>
        <div className="min-w-0 flex-1">
          <p className="truncate leading-tight">
            <span className="font-medium">{projectName(current.project_id)}</span>
            {current.note && <span className="text-dim"> · {current.note}</span>}
          </p>
          <p className="tnum font-mono t-caption leading-tight text-faint">
            since {hhmm(current.started_at)}
          </p>
        </div>
        <button onClick={onStop} disabled={busy} className="btn-primary">
          Stop
        </button>
      </section>
    );
  }

  return (
    <section aria-label="Start a timer" className="panel">
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          if (!projectID) return;
          await onStart(projectID, note.trim());
          setNote("");
        }}
        className="flex items-center gap-2 px-3 py-2"
      >
        <input
          ref={noteRef}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="What are you working on?"
          aria-label="What are you working on?"
          className="min-w-0 flex-1 bg-transparent py-1 t-lead outline-none placeholder:text-faint"
        />
        <kbd className="hidden shrink-0 rounded border border-line px-1.5 font-mono t-caption leading-[1.7] text-faint sm:block">
          n
        </kbd>
        <select
          value={projectID}
          onChange={(e) => setProjectID(e.target.value)}
          aria-label="Project"
          className="select max-w-[11rem] py-1.5"
        >
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        <button type="submit" disabled={busy || !projectID} className="btn-primary">
          Start
        </button>
      </form>
    </section>
  );
}

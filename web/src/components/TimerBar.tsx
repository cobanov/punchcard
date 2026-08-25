import { useEffect, useRef, useState } from "react";
import type { Project, Session } from "../lib/api";
import { clock, elapsed, hhmm } from "../lib/format";

/**
 * The one control that matters.
 *
 * It never leaves the screen, because starting and stopping is what people open
 * punchcard to do. Everything else on the page is read; this is pressed.
 *
 * The running clock is the only large thing in the interface. In a tool where
 * every other number is 11 or 13px, one element at 28px with tight tracking is
 * the whole hierarchy — and it should be the number that is actually moving.
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

  // `n` focuses the note from anywhere. A tool people live in should be
  // reachable without the mouse.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement).tagName;
      if (["INPUT", "TEXTAREA", "SELECT"].includes(tag)) return;
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
      <section
        aria-label="Running timer"
        className="flex items-center gap-4 border-b border-line pb-4"
      >
        <span className="tnum font-mono text-[28px] font-medium leading-none tracking-tight text-punch">
          {clock(elapsed(current))}
        </span>
        <div className="min-w-0 flex-1">
          <p className="truncate">
            <span className="font-medium">{projectName(current.project_id)}</span>
            {current.note && <span className="text-dim"> · {current.note}</span>}
          </p>
          <p className="tnum font-mono text-[11px] text-faint">since {hhmm(current.started_at)}</p>
        </div>
        <button onClick={onStop} disabled={busy} className="btn-primary px-3 py-1.5">
          Stop
        </button>
      </section>
    );
  }

  return (
    <section aria-label="Start a timer" className="border-b border-line pb-4">
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          if (!projectID) return;
          await onStart(projectID, note.trim());
          setNote("");
        }}
        className="flex items-center gap-2"
      >
        <input
          ref={noteRef}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="What are you working on?"
          aria-label="What are you working on?"
          className="min-w-0 flex-1 bg-transparent py-1 text-[15px] outline-none placeholder:text-faint"
        />
        <kbd className="hidden shrink-0 rounded border border-line px-1 font-mono text-[10px] text-faint sm:block">
          n
        </kbd>
        <select
          value={projectID}
          onChange={(e) => setProjectID(e.target.value)}
          aria-label="Project"
          className="field max-w-[11rem]"
        >
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        <button type="submit" disabled={busy || !projectID} className="btn-primary px-3 py-1.5">
          Start
        </button>
      </form>
    </section>
  );
}

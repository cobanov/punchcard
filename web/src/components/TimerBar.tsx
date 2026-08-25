import { useEffect, useRef, useState } from "react";
import type { Project, Session } from "../lib/api";
import { clock, elapsed, hhmm } from "../lib/format";

/**
 * The one control that matters.
 *
 * It never leaves the screen, because starting and stopping is what people open
 * punchcard to do. Everything else on the page is something you look at; this is
 * the thing you press.
 *
 * Starting takes two fields and one key. The project remembers what you used
 * last, so most days you type a sentence and press Enter. Rate, client and
 * repositories are not here — they were settled once, on the project.
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
  const [tick, setTick] = useState(0);
  const noteRef = useRef<HTMLInputElement>(null);

  // The running clock ticks locally. The data behind it refreshes on its own
  // schedule; a timer that only moved when the network answered would look
  // broken every time the network was slow.
  useEffect(() => {
    if (!current?.running) return;
    const id = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(id);
  }, [current?.running]);
  void tick;

  // Default to the most recently used project — nine times in ten it is the
  // right one, and picking it again is friction for nothing.
  useEffect(() => {
    if (!projectID && projects.length) setProjectID(projects[0]!.id);
  }, [projects, projectID]);

  // `n` focuses the note from anywhere, the way a keyboard-driven tool should.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement;
      const typing = ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName);
      if (!typing && e.key === "n") {
        e.preventDefault();
        noteRef.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  if (current?.running) {
    return (
      <div className="flex items-center gap-4 rounded-lg border border-line bg-card px-4 py-3">
        <span className="font-mono text-lg font-medium tabular-nums text-punch">
          {clock(elapsed(current))}
        </span>
        <div className="min-w-0 flex-1">
          <div className="truncate">
            <span className="font-medium">{projectName(current.project_id)}</span>
            <span className="text-dim"> · {current.note || "—"}</span>
          </div>
          <div className="font-mono text-[11px] text-faint">since {hhmm(current.started_at)}</div>
        </div>
        <button
          onClick={onStop}
          disabled={busy}
          className="rounded-md bg-punch px-4 py-2 font-medium text-ink transition hover:brightness-110 disabled:opacity-50"
        >
          Stop
        </button>
      </div>
    );
  }

  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        if (!projectID) return;
        await onStart(projectID, note.trim());
        setNote("");
      }}
      className="flex items-center gap-2 rounded-lg border border-line bg-card px-2 py-2"
    >
      <input
        ref={noteRef}
        value={note}
        onChange={(e) => setNote(e.target.value)}
        placeholder="What are you working on?"
        aria-label="What are you working on?"
        className="min-w-0 flex-1 bg-transparent px-2 py-1.5 outline-none placeholder:text-faint"
      />
      <select
        value={projectID}
        onChange={(e) => setProjectID(e.target.value)}
        aria-label="Project"
        className="max-w-[12rem] rounded-md bg-raise px-2 py-1.5 text-text outline-none"
      >
        {projects.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </select>
      <button
        type="submit"
        disabled={busy || !projectID}
        className="rounded-md bg-punch px-4 py-1.5 font-medium text-ink transition hover:brightness-110 disabled:opacity-50"
      >
        Start
      </button>
    </form>
  );
}

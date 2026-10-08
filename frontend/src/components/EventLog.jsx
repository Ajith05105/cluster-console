import { useState } from "react";
import { utcTime } from "../format.js";

// The kinds of event the console records, and what each means.
const KINDS = [
  { kind: "mark", label: "Marks", meaning: "a moment someone named" },
  { kind: "action", label: "Actions", meaning: "something done through this console" },
  { kind: "load", label: "Load", meaning: "the camera load was started, changed or stopped" },
  { kind: "warning", label: "Warnings", meaning: "a warning started or ended" },
  { kind: "cluster", label: "Cluster", meaning: "something the console saw happen in the cluster" },
];

// EventLog lists what has happened, newest first. Every line is also written
// to the events CSV with the same UTC timestamp.
export default function EventLog({ events }) {
  // Which kinds are shown. Everything to begin with.
  const [hidden, setHidden] = useState(() => new Set());

  const toggle = (kind) => {
    const next = new Set(hidden);
    if (next.has(kind)) {
      next.delete(kind);
    } else {
      next.add(kind);
    }
    setHidden(next);
  };

  const shown = events.filter((event) => !hidden.has(event.kind)).reverse();

  return (
    <section className="card event-log">
      <div className="section-title">
        <h2>Event log</h2>
        <div className="filters" role="group" aria-label="Kinds of event to show">
          {KINDS.map(({ kind, label, meaning }) => (
            <button
              key={kind}
              type="button"
              className={hidden.has(kind) ? "filter" : "filter filter-on"}
              aria-pressed={!hidden.has(kind)}
              title={`${label}: ${meaning}`}
              onClick={() => toggle(kind)}
            >
              <span className={`tag tag-${kind}`} aria-hidden="true" />
              {label}
            </button>
          ))}
        </div>
      </div>

      {shown.length === 0 ? (
        <p className="panel-status">{events.length === 0 ? "Nothing has happened yet." : "No events of the chosen kinds."}</p>
      ) : (
        <ol className="events">
          {shown.map((event, index) => (
            <li key={`${event.time}-${index}`} className={`event event-${event.kind}`}>
              <time dateTime={event.time}>{utcTime(event.time)}</time>
              <span className={`tag tag-${event.kind}`} title={event.kind} />
              <span className="event-kind">{event.kind}</span>
              <span className="event-message">{event.message}</span>
            </li>
          ))}
        </ol>
      )}
      <p className="hint">Times are UTC. Newest first; the most recent {events.length} events are kept on the page.</p>
    </section>
  );
}

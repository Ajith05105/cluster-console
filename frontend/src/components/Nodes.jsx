import { useEffect, useState } from "react";
import { cores, howLong, plural, secondsSince } from "../format.js";

// Nodes draws the cluster: one card per node with a square for each pod on
// it, and a "waiting" box for pods that have no node to run on.
//
// Every worker gets a full card, because workers are where the workload
// runs. Control-plane nodes never run the workload, so they are listed in
// one compact row underneath.
export default function Nodes({ cluster, lastSecond, selectedPod, onSelectPod }) {
  const now = useNow();

  if (!cluster) {
    return <section className="card">Waiting for the console…</section>;
  }

  const testNodes = cluster.nodes.filter((node) => node.pool === "test" || node.pool === "unknown");
  const otherNodes = cluster.nodes.filter((node) => node.pool !== "test" && node.pool !== "unknown");
  const perNode = lastSecond ? lastSecond.per_node || {} : {};

  return (
    <section className="nodes">
      <div className="section-title">
        <h2>Nodes</h2>
        <PodLegend />
      </div>

      {!cluster.sources.nodes && (
        <p className="hint">
          The console is not allowed to read the node list, so nodes are shown only where a pod is running and their
          health is unknown.
        </p>
      )}

      <div className="node-grid">
        {testNodes.map((node) => (
          <NodeCard
            key={node.name}
            node={node}
            now={now}
            framesLastSecond={perNode[node.name]}
            selectedPod={selectedPod}
            onSelectPod={onSelectPod}
          />
        ))}
        <WaitingBox pods={cluster.pending} now={now} />
      </div>

      {otherNodes.length > 0 && (
        <div className="other-nodes">
          <span className="other-nodes-label">Control-plane (never runs the workload):</span>
          {otherNodes.map((node) => (
            <span key={node.name} className="chip" title={`${node.role}, ${cores(node.cpu_millis)}`}>
              <span className={node.ready ? "dot dot-good" : "dot dot-bad"} aria-hidden="true" />
              {node.name}
              {!node.ready && <span className="chip-note">NotReady</span>}
            </span>
          ))}
        </div>
      )}
    </section>
  );
}

// NodeCard is one worker node and its pods.
function NodeCard({ node, now, framesLastSecond, selectedPod, onSelectPod }) {
  const down = node.known && !node.ready;
  const readyPods = node.pods.filter((pod) => pod.ready && !pod.terminating).length;

  return (
    <article className={down ? "card node node-down" : "card node"}>
      <header className="node-head">
        <h3>{node.name}</h3>
        {node.known ? (
          <span className={down ? "status status-bad" : "status status-good"}>
            {down
              ? `NotReady${node.not_ready_since ? ` for ${howLong(secondsSince(node.not_ready_since, now))}` : ""}`
              : "Ready"}
          </span>
        ) : (
          <span className="status status-quiet">health unknown</span>
        )}
      </header>

      <p className="node-facts">{node.known ? `worker · ${cores(node.cpu_millis)}` : "worker"}</p>

      <div className="pods">
        {node.pods.length === 0 && <span className="pods-empty">no pods</span>}
        {node.pods.map((pod) => (
          <PodSquare
            key={pod.name}
            pod={pod}
            nodeDown={down}
            selected={pod.name === selectedPod}
            onSelect={() => onSelectPod(pod.name === selectedPod ? null : pod.name)}
          />
        ))}
      </div>

      <footer className="node-foot">
        {down ? (
          // Kubernetes goes on listing these pods as ready for a while after
          // their node stops answering. Say what is actually true.
          <span>{plural(node.pods.length, "pod")} on a node that is not responding</span>
        ) : (
          <span>
            {readyPods} of {plural(node.pods.length, "pod")} ready
          </span>
        )}
        <span>{framesLastSecond ? `${framesLastSecond} frames in the last second` : "no frames in the last second"}</span>
      </footer>
    </article>
  );
}

// PodSquare is one pod. Its look says what state it is in, and clicking it
// selects it for the "Delete pod" button.
//
// The states, and how each is drawn (shape as well as colour, so they can be
// told apart without relying on colour alone):
//   ready        solid green square
//   starting     pale square with a dashed edge
//   stopping     salmon square with a cross
//   unreachable  the node is NotReady: faded square with a raspberry edge
function PodSquare({ pod, nodeDown, selected, onSelect }) {
  let state = "starting";
  let label = pod.detail || "starting";
  if (pod.terminating) {
    state = "stopping";
    label = "shutting down";
  } else if (nodeDown) {
    state = "unreachable";
    label = "on a node that is NotReady";
  } else if (pod.ready) {
    state = "ready";
    label = "ready";
  }

  return (
    <button
      type="button"
      className={`pod pod-${state}${selected ? " pod-selected" : ""}`}
      title={`${pod.name}\n${label}`}
      aria-label={`Pod ${pod.name}, ${label}${selected ? ", selected" : ""}`}
      aria-pressed={selected}
      onClick={onSelect}
    >
      {state === "stopping" ? "×" : ""}
    </button>
  );
}

// WaitingBox holds the pods that have been created but have no node. The
// autoscaler does not know whether the nodes have room; this box is where
// "the cluster is full" becomes visible.
function WaitingBox({ pods, now }) {
  // Group the pods by the scheduler's reason, so the reason is said once.
  const reasons = {};
  for (const pod of pods) {
    const reason = pod.pending_reason || "waiting for the scheduler";
    reasons[reason] = (reasons[reason] || 0) + 1;
  }
  // How long the longest-waiting pod has been waiting.
  const since = pods.map((pod) => pod.pending_since).filter(Boolean).sort()[0];

  return (
    <article className={pods.length > 0 ? "card node waiting waiting-busy" : "card node waiting"}>
      <header className="node-head">
        <h3>Waiting for a node</h3>
        <span className="status status-quiet">{plural(pods.length, "pod")}</span>
      </header>

      <p className="node-facts">Pods that are Pending because no node has room for them.</p>

      <div className="pods">
        {pods.length === 0 && <span className="pods-empty">none</span>}
        {pods.map((pod) => (
          <span
            key={pod.name}
            className="pod pod-waiting"
            title={`${pod.name}\n${pod.pending_message || pod.pending_reason || ""}`}
            aria-label={`Pod ${pod.name}, waiting: ${pod.pending_reason}`}
          />
        ))}
      </div>

      <footer className="node-foot">
        {pods.length === 0 ? (
          <span>Nothing is waiting.</span>
        ) : (
          <span>
            {Object.entries(reasons)
              .map(([reason, count]) => `${count} × ${reason}`)
              .join(", ")}
            {since ? ` · longest wait ${howLong(secondsSince(since, now))}` : ""}
          </span>
        )}
      </footer>
    </article>
  );
}

// PodLegend explains the squares.
function PodLegend() {
  return (
    <div className="pod-legend" aria-label="What the pod squares mean">
      <span>
        <span className="pod pod-ready pod-key" /> ready
      </span>
      <span>
        <span className="pod pod-starting pod-key" /> starting
      </span>
      <span>
        <span className="pod pod-stopping pod-key">×</span> shutting down
      </span>
      <span>
        <span className="pod pod-waiting pod-key" /> waiting for a node
      </span>
      <span>
        <span className="pod pod-unreachable pod-key" /> on a NotReady node
      </span>
    </div>
  );
}

// useNow returns the current time and re-renders once a second, so "for 42 s"
// keeps counting between messages from the console.
function useNow() {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  return now;
}

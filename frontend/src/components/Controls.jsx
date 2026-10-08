import { useEffect, useRef, useState } from "react";
import { listFiles } from "../api.js";
import { plural, round1 } from "../format.js";

// Controls is the right-hand column: everything that changes something.
//
// Each panel sends its change through `act(path, body)`, which does the POST
// and shows the console's answer in the line at the bottom of the column.
// When `canChange` is false (no accepted token, or read-only mode) every
// control is switched off.
export default function Controls({ hello, snapshot, canChange, readOnly, act, notice, selectedPod, allPods }) {
  if (!hello || !snapshot) {
    return <section className="card">Waiting for the console…</section>;
  }
  const { cluster, load } = snapshot;

  return (
    <div className="controls">
      {!canChange && (
        <p className="locked-note">
          {readOnly
            ? "The console is in read-only mode. You can watch, but not change anything."
            : "Enter the access token (top right) to use these controls."}
        </p>
      )}

      {/* fieldset disabled switches off every control inside it at once. */}
      <fieldset className="controls-set" disabled={!canChange}>
        <DeployPanel hello={hello} workload={cluster.workload} act={act} />
        <ScalePanel workload={cluster.workload} hpa={cluster.hpa} act={act} />
        <LoadPanel limits={hello.limits} load={load} act={act} />
        <MarkPanel act={act} />
        <PodPanel selectedPod={selectedPod} allPods={allPods} act={act} />
      </fieldset>

      <FilesPanel />
      <Notice notice={notice} />
    </div>
  );
}

// DeployPanel: choose what to run and with what resources, then Deploy or
// Remove. Only entries from the console's catalog can be chosen.
function DeployPanel({ hello, workload, act }) {
  const { catalog } = hello;
  const [key, setKey] = useState(catalog.default);
  const item = catalog.items.find((entry) => entry.key === key) || catalog.items[0];

  // The editable fields start at the chosen entry's defaults.
  const [cpu, setCpu] = useState(item.cpu_request);
  const [memory, setMemory] = useState(item.memory_request);
  const [maxPods, setMaxPods] = useState(item.hpa.max_replicas);
  const [fast, setFast] = useState(false);
  const [busy, setBusy] = useState(false);

  // Choosing a different image resets the fields to that image's defaults.
  const choose = (nextKey) => {
    const next = catalog.items.find((entry) => entry.key === nextKey);
    setKey(nextKey);
    setCpu(next.cpu_request);
    setMemory(next.memory_request);
    setMaxPods(next.hpa.max_replicas);
  };

  const deploy = async () => {
    setBusy(true);
    await act("/api/deploy", {
      image_key: key,
      cpu_request: cpu.trim(),
      memory_request: memory.trim(),
      max_replicas: Number(maxPods),
      fast_node_death: fast,
    });
    setBusy(false);
  };

  const remove = async () => {
    setBusy(true);
    await act("/api/undeploy");
    setBusy(false);
  };

  const { baseline, fast: fastSeconds } = catalog.node_death_seconds;

  return (
    <section className="card panel">
      <h2>Workload</h2>

      <p className="panel-status">
        {workload ? (
          <>
            Running <strong>{workload.name}</strong>: CPU {workload.cpu_request}, memory {workload.memory_request}, pods
            on a dead node replaced after {workload.node_death_seconds} s.
          </>
        ) : (
          "Nothing is deployed."
        )}
      </p>

      <label className="field">
        <span>Image</span>
        <select value={key} onChange={(event) => choose(event.target.value)}>
          {catalog.items.map((entry) => (
            <option key={entry.key} value={entry.key}>
              {entry.name}
            </option>
          ))}
        </select>
      </label>

      <div className="field-row">
        <label className="field">
          <span>CPU per pod</span>
          <input value={cpu} onChange={(event) => setCpu(event.target.value)} spellCheck={false} title="Like 500m (half a core) or 1 (one core)" />
        </label>
        <label className="field">
          <span>Memory per pod</span>
          <input value={memory} onChange={(event) => setMemory(event.target.value)} spellCheck={false} title="Like 128Mi or 1Gi" />
        </label>
        <label className="field field-narrow">
          <span>Max pods</span>
          <input
            type="number"
            min={1}
            max={hello.limits.max_replicas}
            value={maxPods}
            onChange={(event) => setMaxPods(event.target.value)}
          />
        </label>
      </div>

      <label className="check">
        <input type="checkbox" checked={fast} onChange={(event) => setFast(event.target.checked)} />
        <span>
          Replace pods on a dead node after {fastSeconds} s <span className="hint-inline">(not {baseline} s)</span>
        </span>
      </label>

      <div className="button-row">
        <button type="button" className="button" onClick={deploy} disabled={busy}>
          {workload ? "Deploy (replaces current)" : "Deploy"}
        </button>
        <button type="button" className="button button-danger" onClick={remove} disabled={busy || !workload}>
          Remove
        </button>
      </div>
    </section>
  );
}

// ScalePanel: the replica stepper.
//
// The number is a floor, not a fixed count: the autoscaler may run more pods
// than this when load calls for it, but never fewer.
function ScalePanel({ workload, hpa, act }) {
  // What the cluster currently has as the floor.
  const current = hpa ? hpa.min : workload ? workload.desired : 1;
  const max = hpa ? hpa.max : 1;

  // `chosen` is what the stepper shows. It follows the cluster, except for
  // the short time between a click and the console confirming it.
  const [chosen, setChosen] = useState(current);
  const pending = useRef(null);
  // Always holds the newest value from the cluster, for use after a wait.
  const latest = useRef(current);
  latest.current = current;

  useEffect(() => {
    if (pending.current === null) setChosen(current);
  }, [current]);

  // Clicking quickly several times should send one request, not several.
  // So wait half a second after the last click, then send.
  const change = (next) => {
    const value = Math.min(max, Math.max(1, next));
    setChosen(value);
    clearTimeout(pending.current);
    pending.current = setTimeout(async () => {
      const result = await act("/api/scale", { replicas: value });
      pending.current = null;
      // If the console refused, show the real number again.
      if (!result.ok) setChosen(latest.current);
    }, 500);
  };

  const disabled = !workload;

  return (
    <section className="card panel">
      <h2>Pods</h2>
      <div className="stepper">
        <button type="button" className="button stepper-button" aria-label="One pod fewer" onClick={() => change(chosen - 1)} disabled={disabled || chosen <= 1}>
          −
        </button>
        <output className="stepper-value" aria-live="polite">
          {disabled ? "–" : chosen}
        </output>
        <button type="button" className="button stepper-button" aria-label="One more pod" onClick={() => change(chosen + 1)} disabled={disabled || chosen >= max}>
          +
        </button>
        <div className="stepper-jumps">
          <button type="button" className="button button-plain" onClick={() => change(1)} disabled={disabled || chosen === 1}>
            Min (1)
          </button>
          <button type="button" className="button button-plain" onClick={() => change(max)} disabled={disabled || chosen === max}>
            Max ({max})
          </button>
        </div>
      </div>
      <p className="panel-status">
        {disabled ? (
          "Deploy a workload first."
        ) : (
          <>
            At least <strong>{plural(chosen, "pod")}</strong>; {workload.ready} ready of {workload.desired} wanted.
            {hpa && (
              <>
                {" "}
                Autoscaler: up to {hpa.max}
                {hpa.cpu_percent !== null ? `, CPU at ${hpa.cpu_percent}% of request (target ${hpa.target_percent}%)` : ""}.
              </>
            )}
          </>
        )}
      </p>
    </section>
  );
}

// LoadPanel: the virtual cameras. Demand is cameras × fps frames per second.
function LoadPanel({ limits, load, act }) {
  const [cameras, setCameras] = useState(load.running ? load.cameras : 10);
  const [fps, setFps] = useState(load.running ? load.fps : 5);
  const pending = useRef(null);
  // Always holds the newest camera count from the console.
  const latestCameras = useRef(load.cameras);
  latestCameras.current = load.cameras;

  // If load is changed from somewhere else (another browser), follow it.
  useEffect(() => {
    if (load.running && pending.current === null) {
      setCameras(load.cameras);
      setFps(load.fps);
    }
  }, [load.running, load.cameras, load.fps]);

  const fpsNumber = Number(fps);
  const fpsValid = fpsNumber >= limits.min_fps && fpsNumber <= limits.max_fps;
  // The slider stops where cameras × fps would pass the console's limit.
  const maxCameras = fpsValid ? Math.max(1, Math.min(limits.max_cameras, Math.floor(limits.max_rate / fpsNumber))) : 1;
  const demand = cameras * (fpsValid ? fpsNumber : 0);

  // Moving the slider while load is running changes the number of cameras
  // straight away (after a short pause, so dragging sends one request).
  const slide = (value) => {
    setCameras(value);
    if (!load.running) return;
    clearTimeout(pending.current);
    pending.current = setTimeout(async () => {
      const result = await act("/api/load/cameras", { cameras: value });
      pending.current = null;
      // If the console refused, put the slider back where it really is.
      if (!result.ok) setCameras(latestCameras.current);
    }, 300);
  };

  const start = () => act("/api/load/start", { cameras: Math.min(cameras, maxCameras), fps: fpsNumber });
  const stop = () => act("/api/load/stop");
  // The fps can only change by starting again with the new number.
  const fpsChanged = load.running && fpsValid && fpsNumber !== load.fps;

  return (
    <section className="card panel">
      <h2>Camera load</h2>

      <div className="field-row">
        <label className="field">
          <span>
            Cameras: <strong>{Math.min(cameras, maxCameras)}</strong> <span className="hint-inline">(up to {maxCameras})</span>
          </span>
          <input
            type="range"
            min={1}
            max={maxCameras}
            value={Math.min(cameras, maxCameras)}
            onChange={(event) => slide(Number(event.target.value))}
          />
        </label>
        <label className="field field-narrow">
          <span>fps each</span>
          <input
            type="number"
            min={limits.min_fps}
            max={limits.max_fps}
            step="0.5"
            value={fps}
            onChange={(event) => setFps(event.target.value)}
          />
        </label>
      </div>
      {!fpsValid && (
        <p className="hint hint-bad">
          Frames per second must be between {limits.min_fps} and {limits.max_fps}.
        </p>
      )}

      <div className="button-row button-row-middle">
        {load.running ? (
          <button type="button" className="button button-danger" onClick={stop}>
            Stop load
          </button>
        ) : (
          <button type="button" className="button" onClick={start} disabled={!fpsValid}>
            Start load
          </button>
        )}
        {fpsChanged && (
          <button type="button" className="button" onClick={start}>
            Apply {fpsNumber} fps
          </button>
        )}
        <p className="demand">
          <span className="demand-value">{round1(demand)}</span>
          <span className="demand-label">frames per second (limit {limits.max_rate})</span>
        </p>
      </div>
      <p className="panel-status">
        {load.running
          ? `Running: ${load.cameras} cameras × ${load.fps} fps = ${round1(load.demand)} frames per second.`
          : "Load is stopped."}
      </p>
    </section>
  );
}

// PodPanel: delete one pod, to show Kubernetes replacing it. Click a pod
// square to choose which; with none chosen, a ready pod is picked at random.
function PodPanel({ selectedPod, allPods, act }) {
  const candidates = allPods.filter((pod) => pod.ready && !pod.terminating);

  const remove = () => {
    const name = selectedPod || candidates[Math.floor(Math.random() * candidates.length)].name;
    act("/api/pod/delete", { name });
  };

  return (
    <section className="card panel">
      <div className="section-title section-title-tight">
        <h2>Delete a pod</h2>
        <button type="button" className="button button-danger" onClick={remove} disabled={!selectedPod && candidates.length === 0}>
          {selectedPod ? "Delete selected pod" : "Delete a random pod"}
        </button>
      </div>
      <p className="panel-status">
        {selectedPod ? (
          <>
            Selected: <code>{selectedPod}</code>
          </>
        ) : (
          "Click a pod square to choose which one."
        )}
      </p>
    </section>
  );
}

// MarkPanel: add a named moment to the event log, for things the console
// cannot see for itself (someone pulling a power cable).
function MarkPanel({ act }) {
  const [custom, setCustom] = useState("");
  const mark = (label) => act("/api/mark", { label });

  const submit = (event) => {
    event.preventDefault();
    mark(custom.trim());
    setCustom("");
  };

  return (
    <section className="card panel">
      <h2>Mark a moment</h2>
      <div className="button-row button-row-even">
        {["node plugged in", "power cut", "power restored"].map((label) => (
          <button key={label} type="button" className="button button-small" onClick={() => mark(label)}>
            {label}
          </button>
        ))}
      </div>
      <form className="field-row" onSubmit={submit}>
        <input
          aria-label="Your own words for a mark"
          placeholder="or your own words"
          value={custom}
          maxLength={80}
          onChange={(event) => setCustom(event.target.value)}
        />
        <button type="submit" className="button button-plain field-button" disabled={!custom.trim()}>
          Mark
        </button>
      </form>
    </section>
  );
}

// Notice shows the console's answer to the last button pressed. It is pinned
// to the bottom corner of the window so it is seen wherever the page is
// scrolled to. A success fades away after a few seconds; a refusal stays
// until it is closed or another button is pressed.
function Notice({ notice }) {
  // Which notice (by its time) has been closed or has timed out.
  const [hiddenAt, setHiddenAt] = useState(null);

  useEffect(() => {
    if (!notice || !notice.ok) return undefined;
    const timer = setTimeout(() => setHiddenAt(notice.at), 6000);
    return () => clearTimeout(timer);
  }, [notice]);

  if (!notice || hiddenAt === notice.at) return null;
  return (
    <div className={notice.ok ? "notice" : "notice notice-bad"} role={notice.ok ? "status" : "alert"}>
      <span aria-hidden="true">{notice.ok ? "✓" : "⚠"}</span>
      <span className="notice-text">{notice.text}</span>
      <button type="button" className="notice-close" aria-label="Close this message" onClick={() => setHiddenAt(notice.at)}>
        ×
      </button>
    </div>
  );
}

// FilesPanel lists the CSV files the console has written, for downloading.
// These are what the poster graphs are made from.
function FilesPanel() {
  const [files, setFiles] = useState(null);
  const refresh = () => listFiles().then(setFiles);

  useEffect(() => {
    refresh();
  }, []);

  return (
    <section className="card panel">
      <div className="section-title">
        <h2>Recorded data (CSV)</h2>
        <button type="button" className="button button-plain" onClick={refresh}>
          Refresh
        </button>
      </div>
      {files === null && <p className="panel-status">Loading…</p>}
      {files !== null && files.length === 0 && <p className="panel-status">No files yet.</p>}
      {files !== null && files.length > 0 && (
        <ul className="files">
          {files.slice(0, 6).map((file) => (
            <li key={file.name}>
              <a href={`/api/files/${encodeURIComponent(file.name)}`} download>
                {file.name}
              </a>
              <span className="file-size">{Math.max(1, Math.round(file.bytes / 1024))} KB</span>
            </li>
          ))}
        </ul>
      )}
      <p className="hint">The newest pair is the current run. Times in the files are UTC.</p>
    </section>
  );
}

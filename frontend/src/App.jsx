import { useCallback, useEffect, useState } from "react";
import { loadToken, post, saveToken } from "./api.js";
import { useConsole } from "./useConsole.js";
import Header from "./components/Header.jsx";
import WarningBanner from "./components/WarningBanner.jsx";
import Nodes from "./components/Nodes.jsx";
import Charts from "./components/Charts.jsx";
import Controls from "./components/Controls.jsx";
import EventLog from "./components/EventLog.jsx";

// App is the whole page. It owns three things and passes them down:
//
//   1. the live state of the cluster          (from useConsole)
//   2. the access token and whether it works  (needed for every change)
//   3. which pod square is selected           (for the delete-a-pod button)
//
// Layout, top to bottom:
//   header, warning banner, then two columns:
//     left:  node cards, the two charts, the event log
//     right: the controls
export default function App() {
  const console_ = useConsole();
  const { hello, snapshot, stats, events, warnings, connected } = console_;

  // --- The access token ---------------------------------------------------
  const [token, setToken] = useState(loadToken);
  // "unknown" until checked, then "accepted" or "refused".
  const [tokenState, setTokenState] = useState("unknown");

  // Ask the console whether the token is right, whenever it changes.
  useEffect(() => {
    let cancelled = false;
    if (!token) {
      setTokenState("unknown");
      return;
    }
    post("/api/auth/check", undefined, token).then((result) => {
      if (!cancelled) setTokenState(result.ok ? "accepted" : "refused");
    });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const changeToken = (value) => {
    saveToken(value);
    setToken(value);
  };

  // Changes are possible only if the console allows them at all (not
  // read-only) and our token has been accepted.
  const readOnly = hello ? hello.read_only : true;
  const canChange = !readOnly && tokenState === "accepted";

  // --- Sending a change -----------------------------------------------------
  // The last reply from the console, shown under the controls.
  const [notice, setNotice] = useState(null);

  // act sends one POST and shows what the console said. Every button on the
  // page goes through here.
  const act = useCallback(
    async (path, body) => {
      const result = await post(path, body, token);
      setNotice({ ok: result.ok, text: result.ok ? result.message : result.error, at: Date.now() });
      if (result.status === 401) setTokenState("refused");
      return result;
    },
    [token],
  );

  // --- The selected pod -----------------------------------------------------
  const [chosenPod, setSelectedPod] = useState(null);

  // The selection only counts while that pod still exists.
  const allPods = snapshot ? snapshot.cluster.nodes.flatMap((node) => node.pods) : [];
  const selectedPod = allPods.some((pod) => pod.name === chosenPod) ? chosenPod : null;

  // The most recent second of numbers, for "frames per second" on each node.
  const lastSecond = stats.length > 0 ? stats[stats.length - 1] : null;

  return (
    <div className="page">
      <Header
        connected={connected}
        snapshot={snapshot}
        readOnly={readOnly}
        token={token}
        tokenState={tokenState}
        onToken={changeToken}
      />

      <WarningBanner warnings={warnings} connected={connected} />

      <div className="columns">
        <main className="left">
          <Nodes
            cluster={snapshot ? snapshot.cluster : null}
            lastSecond={lastSecond}
            selectedPod={selectedPod}
            onSelectPod={setSelectedPod}
          />
          <Charts stats={stats} events={events} />
          <EventLog events={events} />
        </main>

        <aside className="right">
          <Controls
            hello={hello}
            snapshot={snapshot}
            canChange={canChange}
            readOnly={readOnly}
            act={act}
            notice={notice}
            selectedPod={selectedPod}
            allPods={allPods}
          />
        </aside>
      </div>
    </div>
  );
}

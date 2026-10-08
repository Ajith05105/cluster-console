import { useState } from "react";

// Header is the strip across the top: the title, whether the live stream is
// connected, what Argo CD says, and the box for the access token.
export default function Header({ connected, snapshot, readOnly, token, tokenState, onToken }) {
  return (
    <header className="header">
      <h1>Cluster console</h1>

      <span className={connected ? "pill pill-good" : "pill pill-bad"}>
        <span className="dot" aria-hidden="true" />
        {connected ? "Live" : "Reconnecting…"}
      </span>

      <ArgoSummary cluster={snapshot ? snapshot.cluster : null} />

      <div className="header-space" />

      {readOnly ? (
        <span className="pill pill-quiet" title="The console is set to refuse every change.">
          Read-only: changes are switched off
        </span>
      ) : (
        <TokenBox token={token} tokenState={tokenState} onToken={onToken} />
      )}
    </header>
  );
}

// ArgoSummary says in a few words whether Argo CD's applications match git.
function ArgoSummary({ cluster }) {
  if (!cluster) return null;
  if (!cluster.sources.argo) {
    return <span className="pill pill-quiet">Argo CD: not readable</span>;
  }
  const outOfSync = cluster.argo.filter((app) => app.sync === "OutOfSync");
  if (outOfSync.length === 0) {
    return <span className="pill pill-quiet">Argo CD: {cluster.argo.length} apps in sync</span>;
  }
  return (
    <span className="pill pill-bad">
      <span className="dot" aria-hidden="true" />
      Argo CD out of sync: {outOfSync.map((app) => app.name).join(", ")}
    </span>
  );
}

// TokenBox is where the access token is typed in. Without an accepted token
// the page can be watched but nothing can be changed.
function TokenBox({ token, tokenState, onToken }) {
  const [draft, setDraft] = useState("");

  if (tokenState === "accepted") {
    return (
      <span className="token">
        <span className="pill pill-good">
          <span className="dot" aria-hidden="true" />
          Unlocked
        </span>
        <button className="button button-plain" onClick={() => onToken("")}>
          Lock
        </button>
      </span>
    );
  }

  const submit = (event) => {
    event.preventDefault();
    onToken(draft.trim());
    setDraft("");
  };

  return (
    <form className="token" onSubmit={submit}>
      {tokenState === "refused" && token && <span className="token-problem">That token was not accepted.</span>}
      <label htmlFor="token">Access token</label>
      <input
        id="token"
        type="password"
        autoComplete="off"
        placeholder="needed to make changes"
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
      />
      <button className="button" type="submit" disabled={!draft.trim()}>
        Unlock
      </button>
    </form>
  );
}

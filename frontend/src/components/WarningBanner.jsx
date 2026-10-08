import { utcTime } from "../format.js";

// WarningBanner is the full-width strip under the header. It is always
// there, so the page does not jump when a warning appears: calm when there
// is nothing to report, loud when there is.
//
// Each warning comes from the console with a ready-made reason sentence, for
// example "6 pods Pending for 21 s with no node to run on: Insufficient cpu".
export default function WarningBanner({ warnings, connected }) {
  if (!connected && warnings.length === 0) {
    return (
      <section className="banner banner-quiet" aria-live="polite">
        Not connected to the console. Warnings cannot be shown until it reconnects.
      </section>
    );
  }

  if (warnings.length === 0) {
    return (
      <section className="banner banner-clear" aria-live="polite">
        <span className="banner-icon" aria-hidden="true">
          ✓
        </span>
        No warnings
      </section>
    );
  }

  return (
    <section className="banner banner-warning" role="alert">
      <ul>
        {warnings.map((warning) => (
          <li key={warning.id}>
            <span className="banner-icon" aria-hidden="true">
              ⚠
            </span>
            <span className="banner-reason">{warning.reason}</span>
            <span className="banner-since">since {utcTime(warning.since)} UTC</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

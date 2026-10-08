// Small helpers for showing times and numbers.
//
// All times on the page are shown in UTC, to match the CSV files that the
// poster graphs are made from.

// utcTime("2026-10-08T06:30:27.001Z") -> "06:30:27"
export function utcTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toISOString().slice(11, 19);
}

// howLong(75) -> "1 min 15 s". Used for "NotReady for ...".
export function howLong(seconds) {
  const whole = Math.max(0, Math.floor(seconds));
  if (whole < 60) return `${whole} s`;
  if (whole < 3600) return `${Math.floor(whole / 60)} min ${String(whole % 60).padStart(2, "0")} s`;
  return `${Math.floor(whole / 3600)} h ${String(Math.floor((whole % 3600) / 60)).padStart(2, "0")} min`;
}

// secondsSince("2026-10-08T06:30:27Z", now) -> seconds between then and now.
export function secondsSince(value, now = Date.now()) {
  return (now - new Date(value).getTime()) / 1000;
}

// round1(42.4567) -> "42.5"; whole numbers stay whole: round1(50) -> "50".
export function round1(value) {
  if (value === null || value === undefined || Number.isNaN(value)) return "–";
  return String(Math.round(value * 10) / 10);
}

// plural(1, "pod") -> "1 pod"; plural(3, "pod") -> "3 pods".
export function plural(count, word) {
  return `${count} ${word}${count === 1 ? "" : "s"}`;
}

// cores(4000) -> "4 cores"; cores(500) -> "0.5 cores".
export function cores(millis) {
  const value = millis / 1000;
  return `${Math.round(value * 10) / 10} ${value === 1 ? "core" : "cores"}`;
}

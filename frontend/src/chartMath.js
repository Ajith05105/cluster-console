// The arithmetic behind the two charts: choosing round axis numbers and
// turning rows of data into SVG path strings. No React here, so it is easy
// to test (see chartMath.test.js).

// niceScale picks a round top for the vertical axis and the tick values to
// label, for data whose largest value is maxValue.
//
//   niceScale(53)  -> { max: 60, ticks: [0, 20, 40, 60] }
//   niceScale(14, { wholeNumbers: true }) -> { max: 15, ticks: [0, 5, 10, 15] }
//
// wholeNumbers keeps the ticks to whole numbers, for counting pods.
export function niceScale(maxValue, { wholeNumbers = false, targetTicks = 4 } = {}) {
  if (!(maxValue > 0)) {
    return { max: 1, ticks: [0, 1] };
  }
  // The tick spacing is 1, 2 or 5 times a power of ten: the smallest such
  // number that needs no more than targetTicks steps to pass maxValue.
  const rough = maxValue / targetTicks;
  const power = Math.pow(10, Math.floor(Math.log10(rough)));
  let step = [1, 2, 5, 10].map((m) => m * power).find((candidate) => candidate >= rough);
  if (wholeNumbers) {
    step = Math.max(1, Math.round(step));
  }
  const max = Math.ceil(maxValue / step) * step;
  const ticks = [];
  for (let value = 0; value <= max + step / 2; value += step) {
    ticks.push(Math.round(value * 1000) / 1000);
  }
  return { max, ticks };
}

// timeTicks picks the times to label on the horizontal axis between two
// times (both in milliseconds). The spacing is a round amount (10 s, 30 s,
// 1 min...) chosen so there are at most maxTicks labels.
export function timeTicks(start, end, maxTicks = 7) {
  const spacings = [10, 15, 30, 60, 120, 300, 600, 900].map((s) => s * 1000);
  const spacing = spacings.find((s) => (end - start) / s <= maxTicks) || spacings[spacings.length - 1];
  const ticks = [];
  for (let t = Math.ceil(start / spacing) * spacing; t <= end; t += spacing) {
    ticks.push(t);
  }
  return ticks;
}

// A gap of more than this between two rows means the console was not
// recording (it was restarting, say). The line is broken there instead of
// being drawn straight across as if nothing happened.
export const MAX_GAP_MS = 2500;

// runs splits rows into stretches with no gap in them.
export function runs(rows) {
  const out = [];
  let current = [];
  for (const row of rows) {
    if (current.length > 0 && row.t - current[current.length - 1].t > MAX_GAP_MS) {
      out.push(current);
      current = [];
    }
    current.push(row);
  }
  if (current.length > 0) out.push(current);
  return out;
}

// corners turns one run of rows into the [x, y] points of a line.
//
// With `step` the line holds each value flat until the next row and then
// jumps, like a staircase. That is the honest shape for a count of pods,
// which is never "between" two whole numbers.
function corners(run, x, yOf, step) {
  const points = [];
  run.forEach((row, index) => {
    const px = x(row.t);
    const py = yOf(row);
    if (step && index > 0) {
      points.push([px, points[points.length - 1][1]]);
    }
    points.push([px, py]);
  });
  return points;
}

const join = (points) => points.map(([px, py]) => `${px.toFixed(1)},${py.toFixed(1)}`).join("L");

// linePath returns the SVG path for a line through the rows.
//   x     turns a time into a horizontal position
//   yOf   turns a row into a vertical position
export function linePath(rows, x, yOf, { step = false } = {}) {
  return runs(rows)
    .map((run) => "M" + join(corners(run, x, yOf, step)))
    .join("");
}

// bandPath returns the SVG path for a filled band between two lines: along
// the top from left to right, then back along the bottom.
export function bandPath(rows, x, yLowOf, yHighOf, { step = false } = {}) {
  return runs(rows)
    .map((run) => {
      const top = corners(run, x, yHighOf, step);
      const bottom = corners(run, x, yLowOf, step).reverse();
      return "M" + join([...top, ...bottom]) + "Z";
    })
    .join("");
}

// nearestRow returns the row whose time is closest to t, or null if there
// are no rows. Used to read off values under the pointer.
export function nearestRow(rows, t) {
  let best = null;
  for (const row of rows) {
    if (best === null || Math.abs(row.t - t) < Math.abs(best.t - t)) {
      best = row;
    }
  }
  return best;
}

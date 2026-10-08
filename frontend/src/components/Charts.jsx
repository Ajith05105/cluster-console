import { useEffect, useMemo, useRef, useState } from "react";
import { bandPath, linePath, nearestRow, niceScale, timeTicks } from "../chartMath.js";
import { round1, utcTime } from "../format.js";

// Charts draws the two live charts, one above the other, sharing one time
// axis:
//
//   1. frames per second: what the cameras ask for (demand) against what the
//      cluster actually processed, with failed frames underneath
//   2. pods over time: how many are ready, how many are waiting for a node,
//      and how many are wanted
//
// They are two charts and not one because frames and pods are different
// units; putting both on one chart would need two vertical scales, which
// invites reading a relationship into where the lines happen to cross.
//
// Colour use follows the page palette: Jet Black for lines, Tea Green and
// Sweet Salmon only as fills under a line (they are too pale to read as
// lines), Raspberry only for failures. Dashes mark the "asked for" lines.
//
// The charts are drawn as plain SVG. The row under each title is both the
// legend and the read-out: it shows the latest values, or the values under
// the pointer when it is over a chart.

const WINDOWS = [
  { minutes: 2, label: "2 min" },
  { minutes: 5, label: "5 min" },
  { minutes: 15, label: "15 min" },
];

// Space around the plotting area, in pixels. Both charts use the same left
// and right margins, which is what keeps their time axes lined up.
const MARGIN = { left: 44, right: 16 };

export default function Charts({ stats, events }) {
  const [minutes, setMinutes] = useState(5);
  // The time under the pointer, shared by both charts, or null.
  const [hoverT, setHoverT] = useState(null);
  const [frameRef, width] = useWidth();

  // Give every row a plain number for its time, once.
  const rows = useMemo(() => stats.map((row) => ({ ...row, t: new Date(row.time).getTime() })), [stats]);

  // The time axis ends at the newest row and reaches back by the chosen
  // window.
  const end = rows.length > 0 ? rows[rows.length - 1].t : Date.now();
  const start = end - minutes * 60 * 1000;
  const visible = useMemo(() => rows.filter((row) => row.t >= start), [rows, start]);

  // Named moments ("power cut") become vertical lines on both charts.
  const marks = useMemo(
    () =>
      events
        .filter((event) => event.kind === "mark")
        .map((event) => ({ t: new Date(event.time).getTime(), label: event.message }))
        .filter((mark) => mark.t >= start && mark.t <= end),
    [events, start, end],
  );

  // The row whose numbers are shown in the legends.
  const hovering = hoverT !== null && visible.length > 0;
  const shown = hovering ? nearestRow(visible, hoverT) : visible[visible.length - 1] || null;

  const shared = { width, start, end, rows: visible, marks, hoverT: hovering ? shown.t : null, onHover: setHoverT };

  return (
    <section className="card charts">
      <div className="section-title">
        <h2>Load and pods over time</h2>
        <div className="segmented" role="group" aria-label="How much time to show">
          {WINDOWS.map((option) => (
            <button
              key={option.minutes}
              type="button"
              className={option.minutes === minutes ? "segment segment-on" : "segment"}
              aria-pressed={option.minutes === minutes}
              onClick={() => setMinutes(option.minutes)}
            >
              {option.label}
            </button>
          ))}
        </div>
      </div>

      <p className="chart-time">
        {shown ? `${hovering ? "At" : "Latest:"} ${utcTime(shown.time)} UTC` : "No data yet"}
        {hovering ? "" : " · move the pointer over a chart to read earlier values"}
      </p>

      <div ref={frameRef} className="chart-frame">
        <FramesChart {...shared} shown={shown} />
        <PodsChart {...shared} shown={shown} />
      </div>
    </section>
  );
}

// FramesChart: frames per second.
function FramesChart({ width, start, end, rows, marks, hoverT, onHover, shown }) {
  const height = 176;
  const top = 26; // room for the labels of marks
  const bottom = 8;

  const biggest = Math.max(1, ...rows.map((row) => Math.max(row.demand, row.processed, row.failed)));
  const scale = niceScale(biggest);
  const x = xScale(start, end, width);
  const y = yScale(scale.max, height, top, bottom);

  return (
    <figure className="chart">
      <figcaption>
        <span className="chart-title">Frames per second</span>
        <Key kind="dashed" label="Demand (cameras × fps)" value={shown ? round1(shown.demand) : "–"} />
        <Key kind="line-on-green" label="Processed" value={shown ? shown.processed : "–"} />
        <Key kind="line-bad" label="Failed" value={shown ? shown.failed : "–"} />
      </figcaption>

      <Plot {...{ width, height, top, bottom, x, y, scale, start, end, marks, hoverT, onHover }} showMarkLabels>
        {/* Drawn back to front. Failed goes first so that when it is zero it
            sits hidden under the other lines along the bottom, and only shows
            when there is something to show. */}
        <path className="fill-green" d={bandPath(rows, x, () => y(0), (row) => y(row.processed))} />
        {/* Failed: raspberry line. */}
        <path className="line line-bad" d={linePath(rows, x, (row) => y(row.failed))} />
        {/* Processed: solid black line on top of its green fill. */}
        <path className="line" d={linePath(rows, x, (row) => y(row.processed))} />
        {/* Demand: dashed black staircase (it only changes when someone changes it). */}
        <path className="line line-dashed" d={linePath(rows, x, (row) => y(row.demand), { step: true })} />
      </Plot>
    </figure>
  );
}

// PodsChart: pods over time. Ready pods are the green block; pods waiting
// for a node are stacked on top in salmon; the dashed line is how many are
// wanted. A gap between the top of the blocks and the dashed line is pods
// that have a node but are still starting.
function PodsChart({ width, start, end, rows, marks, hoverT, onHover, shown }) {
  const height = 166;
  const top = 10;
  const bottom = 24; // room for the time labels

  const biggest = Math.max(1, ...rows.map((row) => Math.max(row.desired_pods, row.ready_pods + row.pending_pods)));
  const scale = niceScale(biggest, { wholeNumbers: true });
  const x = xScale(start, end, width);
  const y = yScale(scale.max, height, top, bottom);
  const step = { step: true };

  return (
    <figure className="chart">
      <figcaption>
        <span className="chart-title">Pods</span>
        <Key kind="dashed" label="Wanted" value={shown ? shown.desired_pods : "–"} />
        <Key kind="line-on-green" label="Ready" value={shown ? shown.ready_pods : "–"} />
        <Key kind="block-salmon" label="Waiting for a node" value={shown ? shown.pending_pods : "–"} />
      </figcaption>

      <Plot {...{ width, height, top, bottom, x, y, scale, start, end, marks, hoverT, onHover }} showTimeLabels>
        {/* Waiting: salmon block sitting on top of the ready block. */}
        <path
          className="fill-salmon"
          d={bandPath(rows, x, (row) => y(row.ready_pods), (row) => y(row.ready_pods + row.pending_pods), step)}
        />
        {/* Ready: green block under a solid black line. */}
        <path className="fill-green" d={bandPath(rows, x, () => y(0), (row) => y(row.ready_pods), step)} />
        <path className="line" d={linePath(rows, x, (row) => y(row.ready_pods), step)} />
        {/* Wanted: dashed black staircase. */}
        <path className="line line-dashed" d={linePath(rows, x, (row) => y(row.desired_pods), step)} />
      </Plot>
    </figure>
  );
}

// Plot draws the parts the two charts have in common: the grid, the axis
// numbers, the mark lines, the pointer line, and an invisible layer that
// follows the pointer. The chart's own lines are passed in as children.
function Plot({ width, height, top, bottom, x, y, scale, start, end, marks, hoverT, onHover, showTimeLabels, showMarkLabels, children }) {
  const plotBottom = height - bottom;

  // Work out which time the pointer is over.
  const follow = (event) => {
    const box = event.currentTarget.getBoundingClientRect();
    const fraction = (event.clientX - box.left) / box.width;
    onHover(start + fraction * (end - start));
  };

  return (
    <svg className="plot" width={width} height={height} role="img">
      {/* Horizontal grid lines and their numbers. */}
      {scale.ticks.map((tick) => (
        <g key={tick}>
          <line className="grid" x1={MARGIN.left} x2={width - MARGIN.right} y1={y(tick)} y2={y(tick)} />
          <text className="axis-text" x={MARGIN.left - 8} y={y(tick)} textAnchor="end" dominantBaseline="middle">
            {tick}
          </text>
        </g>
      ))}

      {/* Time labels along the bottom (only on the lower chart). */}
      {timeTicks(start, end).map((tick) => (
        <g key={tick}>
          <line className="grid" x1={x(tick)} x2={x(tick)} y1={top} y2={plotBottom} />
          {showTimeLabels && (
            // A label right at the edge is lined up by its end, so it is
            // not cut off.
            <text className="axis-text" x={x(tick)} y={plotBottom + 16} textAnchor={x(tick) > width - 40 ? "end" : "middle"}>
              {utcTime(tick)}
            </text>
          )}
        </g>
      ))}

      {children}

      {/* Named moments. The label is written once, on the upper chart. */}
      {marks.map((mark, index) => {
        const nearRightEdge = x(mark.t) > width - 140;
        return (
          <g key={`${mark.t}-${index}`}>
            <line className="mark-line" x1={x(mark.t)} x2={x(mark.t)} y1={showMarkLabels ? 14 + (index % 2) * 11 : top} y2={plotBottom} />
            {showMarkLabels && (
              <text
                className="mark-text"
                x={x(mark.t) + (nearRightEdge ? -4 : 4)}
                y={10 + (index % 2) * 11}
                textAnchor={nearRightEdge ? "end" : "start"}
              >
                {mark.label.length > 26 ? `${mark.label.slice(0, 25)}…` : mark.label}
              </text>
            )}
          </g>
        );
      })}

      {/* The line under the pointer. */}
      {hoverT !== null && <line className="crosshair" x1={x(hoverT)} x2={x(hoverT)} y1={top} y2={plotBottom} />}

      {/* An invisible sheet over the plot that reports where the pointer is. */}
      <rect
        className="pointer-sheet"
        x={MARGIN.left}
        y={top}
        width={Math.max(0, width - MARGIN.left - MARGIN.right)}
        height={Math.max(0, plotBottom - top)}
        onPointerMove={follow}
        onPointerLeave={() => onHover(null)}
      />
    </svg>
  );
}

// Key is one entry in a chart's legend: a small sample of how the series is
// drawn, its name, and its value. The value is in ordinary text colour; the
// sample beside it carries the identity.
function Key({ kind, label, value }) {
  return (
    <span className="key">
      <svg className="key-sample" width="22" height="12" aria-hidden="true">
        {kind === "line-on-green" && <rect className="fill-green" x="0" y="5" width="22" height="7" />}
        {kind === "block-salmon" && <rect className="fill-salmon" x="0" y="1" width="22" height="10" />}
        {kind === "dashed" && <line className="line line-dashed" x1="0" x2="22" y1="6" y2="6" />}
        {kind === "line-on-green" && <line className="line" x1="0" x2="22" y1="5" y2="5" />}
        {kind === "line-bad" && <line className="line line-bad" x1="0" x2="22" y1="6" y2="6" />}
      </svg>
      <span className="key-value">{value}</span>
      <span className="key-label">{label}</span>
    </span>
  );
}

// xScale returns a function that turns a time into a horizontal position.
function xScale(start, end, width) {
  const span = Math.max(1, end - start);
  const inner = Math.max(1, width - MARGIN.left - MARGIN.right);
  return (t) => MARGIN.left + ((t - start) / span) * inner;
}

// yScale returns a function that turns a value into a vertical position.
// (In SVG, y grows downwards, so bigger values get smaller y.)
function yScale(max, height, top, bottom) {
  const inner = height - top - bottom;
  return (value) => top + inner - (value / max) * inner;
}

// useWidth measures an element and keeps the number up to date as the
// window is resized, so the charts always fill the space they are given.
function useWidth() {
  const ref = useRef(null);
  const [width, setWidth] = useState(600);
  useEffect(() => {
    if (!ref.current) return undefined;
    const observer = new ResizeObserver((entries) => setWidth(Math.floor(entries[0].contentRect.width)));
    observer.observe(ref.current);
    return () => observer.disconnect();
  }, []);
  return [ref, width];
}

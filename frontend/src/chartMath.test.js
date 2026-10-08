import { describe, expect, test } from "vitest";
import { bandPath, linePath, MAX_GAP_MS, nearestRow, niceScale, runs, timeTicks } from "./chartMath.js";

describe("niceScale: round numbers for the vertical axis", () => {
  test("picks a round top and evenly spaced ticks", () => {
    expect(niceScale(53)).toEqual({ max: 60, ticks: [0, 20, 40, 60] });
    expect(niceScale(100)).toEqual({ max: 100, ticks: [0, 50, 100] });
    expect(niceScale(600)).toEqual({ max: 600, ticks: [0, 200, 400, 600] });
    expect(niceScale(179)).toEqual({ max: 200, ticks: [0, 50, 100, 150, 200] });
  });

  test("the top is never below the data", () => {
    for (const value of [1, 3, 7, 14, 20, 21, 49, 99, 101, 350, 599]) {
      expect(niceScale(value).max).toBeGreaterThanOrEqual(value);
    }
  });

  test("whole numbers only, for counting pods", () => {
    expect(niceScale(14, { wholeNumbers: true })).toEqual({ max: 15, ticks: [0, 5, 10, 15] });
    expect(niceScale(1, { wholeNumbers: true })).toEqual({ max: 1, ticks: [0, 1] });
    expect(niceScale(3, { wholeNumbers: true }).ticks.every(Number.isInteger)).toBe(true);
  });

  test("no data still gives a usable axis", () => {
    expect(niceScale(0)).toEqual({ max: 1, ticks: [0, 1] });
  });
});

describe("timeTicks: round times for the horizontal axis", () => {
  test("a five-minute window is labelled every minute", () => {
    const start = Date.UTC(2026, 9, 8, 6, 30, 12);
    const ticks = timeTicks(start, start + 5 * 60 * 1000);
    expect(ticks).toHaveLength(5);
    expect(ticks.every((t) => t % 60000 === 0)).toBe(true);
    expect(ticks[0]).toBe(Date.UTC(2026, 9, 8, 6, 31, 0));
  });

  test("never more labels than asked for", () => {
    const start = Date.UTC(2026, 9, 8, 6, 0, 0);
    for (const minutes of [2, 5, 15, 60]) {
      expect(timeTicks(start, start + minutes * 60 * 1000, 7).length).toBeLessThanOrEqual(8);
    }
  });
});

describe("turning rows into SVG paths", () => {
  const x = (t) => t / 1000; // one pixel per second
  const rows = [
    { t: 0, v: 1 },
    { t: 1000, v: 3 },
    { t: 2000, v: 2 },
  ];

  test("a plain line goes point to point", () => {
    expect(linePath(rows, x, (row) => row.v)).toBe("M0.0,1.0L1.0,3.0L2.0,2.0");
  });

  test("a stepped line holds each value until the next row", () => {
    expect(linePath(rows, x, (row) => row.v, { step: true })).toBe("M0.0,1.0L1.0,1.0L1.0,3.0L2.0,3.0L2.0,2.0");
  });

  test("a band goes along the top and back along the bottom", () => {
    expect(bandPath(rows, x, () => 0, (row) => row.v)).toBe("M0.0,1.0L1.0,3.0L2.0,2.0L2.0,0.0L1.0,0.0L0.0,0.0Z");
  });

  test("a gap in the data breaks the line instead of bridging it", () => {
    const gappy = [...rows, { t: 2000 + MAX_GAP_MS + 1, v: 5 }, { t: 2000 + MAX_GAP_MS + 1001, v: 6 }];
    expect(runs(gappy).map((run) => run.length)).toEqual([3, 2]);
    expect(linePath(gappy, x, (row) => row.v).match(/M/g)).toHaveLength(2);
  });

  test("no rows gives an empty path", () => {
    expect(linePath([], x, (row) => row.v)).toBe("");
    expect(bandPath([], x, () => 0, (row) => row.v)).toBe("");
  });
});

describe("nearestRow: reading values under the pointer", () => {
  const rows = [{ t: 1000 }, { t: 2000 }, { t: 3000 }];
  test("finds the closest row in time", () => {
    expect(nearestRow(rows, 1400).t).toBe(1000);
    expect(nearestRow(rows, 1600).t).toBe(2000);
    expect(nearestRow(rows, 99999).t).toBe(3000);
    expect(nearestRow([], 5)).toBeNull();
  });
});

import { describe, expect, test } from "vitest";
import { cores, howLong, plural, round1, secondsSince, utcTime } from "./format.js";

describe("showing times and numbers", () => {
  test("times are shown in UTC whatever the browser's time zone", () => {
    expect(utcTime("2026-10-08T06:30:27.001Z")).toBe("06:30:27");
    expect(utcTime("2026-10-08T19:30:27+13:00")).toBe("06:30:27");
    expect(utcTime(Date.UTC(2026, 9, 8, 23, 59, 59))).toBe("23:59:59");
    expect(utcTime("not a time")).toBe("");
  });

  test("lengths of time", () => {
    expect(howLong(0)).toBe("0 s");
    expect(howLong(42.9)).toBe("42 s");
    expect(howLong(75)).toBe("1 min 15 s");
    expect(howLong(3700)).toBe("1 h 01 min");
    expect(howLong(-5)).toBe("0 s");
    expect(secondsSince("2026-10-08T06:30:00Z", Date.UTC(2026, 9, 8, 6, 30, 42))).toBe(42);
  });

  test("numbers and words", () => {
    expect(round1(42.4567)).toBe("42.5");
    expect(round1(50)).toBe("50");
    expect(round1(null)).toBe("–");
    expect(plural(1, "pod")).toBe("1 pod");
    expect(plural(0, "pod")).toBe("0 pods");
    expect(plural(7, "pod")).toBe("7 pods");
    expect(cores(4000)).toBe("4 cores");
    expect(cores(1000)).toBe("1 core");
    expect(cores(500)).toBe("0.5 cores");
  });
});

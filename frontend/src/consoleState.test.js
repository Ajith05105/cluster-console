import { describe, expect, test } from "vitest";
import { initialState, MAX_EVENTS, MAX_STATS, reduce } from "./consoleState.js";

// Feed a list of messages through the reducer, as the live stream would.
const after = (messages, start = initialState) => messages.reduce(reduce, start);

describe("how stream messages change the page's state", () => {
  test("opening and losing the connection", () => {
    expect(after([{ type: "open" }]).connected).toBe(true);
    expect(after([{ type: "open" }, { type: "closed" }]).connected).toBe(false);
  });

  test("a state message replaces the snapshot and the warnings", () => {
    const snapshot = { cluster: { nodes: [] }, warnings: [{ id: "frames", reason: "too many failed" }] };
    const state = after([{ type: "state", data: snapshot }]);
    expect(state.snapshot).toBe(snapshot);
    expect(state.warnings).toHaveLength(1);
  });

  test("a warnings message replaces the list, including with nothing", () => {
    const state = after([
      { type: "warnings", data: [{ id: "a" }, { id: "b" }] },
      { type: "warnings", data: [] },
    ]);
    expect(state.warnings).toEqual([]);
  });

  test("stats and events are added to the end, oldest first", () => {
    const state = after([
      { type: "stats", data: { sent: 1 } },
      { type: "stats", data: { sent: 2 } },
      { type: "event", data: { message: "first" } },
      { type: "event", data: { message: "second" } },
    ]);
    expect(state.stats.map((row) => row.sent)).toEqual([1, 2]);
    expect(state.events.map((event) => event.message)).toEqual(["first", "second"]);
  });

  test("history replaces what was there, so a reconnect shows nothing twice", () => {
    const state = after([
      { type: "stats", data: { sent: 1 } },
      { type: "event", data: { message: "old" } },
      { type: "history", data: { stats: [{ sent: 7 }, { sent: 8 }], events: [{ message: "from history" }] } },
    ]);
    expect(state.stats.map((row) => row.sent)).toEqual([7, 8]);
    expect(state.events.map((event) => event.message)).toEqual(["from history"]);
  });

  test("only the most recent rows and events are kept", () => {
    let state = initialState;
    for (let i = 0; i < MAX_STATS + 25; i++) state = reduce(state, { type: "stats", data: { sent: i } });
    for (let i = 0; i < MAX_EVENTS + 25; i++) state = reduce(state, { type: "event", data: { message: String(i) } });
    expect(state.stats).toHaveLength(MAX_STATS);
    expect(state.stats[0].sent).toBe(25);
    expect(state.events).toHaveLength(MAX_EVENTS);
    expect(state.events[state.events.length - 1].message).toBe(String(MAX_EVENTS + 24));
  });

  test("an unknown message changes nothing", () => {
    expect(reduce(initialState, { type: "something-new", data: {} })).toBe(initialState);
  });
});

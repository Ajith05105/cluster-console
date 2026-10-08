// Everything the page knows about the cluster, and how each message from the
// live stream changes it.
//
// This file is plain JavaScript with no React in it, so it is easy to test
// (see consoleState.test.js). useConsole.js connects it to the stream.

// How much history the page keeps. The server sends the same amounts.
export const MAX_STATS = 900; // 15 minutes at one row per second
export const MAX_EVENTS = 500;

export const initialState = {
  // true while the live stream is connected
  connected: false,
  // sent once on connecting: the catalog, the limits, whether changes are off
  hello: null,
  // the latest full picture of the cluster (see GET /api/state)
  snapshot: null,
  // one row per second, oldest first
  stats: [],
  // event-log lines, oldest first
  events: [],
  // the warnings that apply right now
  warnings: [],
};

// reduce returns the new state after one message. `message` is
// { type, data }, where type is the stream's message name (or "open" /
// "closed" for the connection itself).
export function reduce(state, message) {
  switch (message.type) {
    case "open":
      return { ...state, connected: true };

    case "closed":
      return { ...state, connected: false };

    case "hello":
      return { ...state, hello: message.data };

    case "history":
      // Sent on every (re)connection. It replaces what we had, so nothing
      // is shown twice after a reconnect.
      return {
        ...state,
        stats: (message.data.stats || []).slice(-MAX_STATS),
        events: (message.data.events || []).slice(-MAX_EVENTS),
      };

    case "state":
      return { ...state, snapshot: message.data, warnings: message.data.warnings || [] };

    case "stats":
      return { ...state, stats: [...state.stats, message.data].slice(-MAX_STATS) };

    case "warnings":
      return { ...state, warnings: message.data || [] };

    case "event":
      return { ...state, events: [...state.events, message.data].slice(-MAX_EVENTS) };

    default:
      return state;
  }
}

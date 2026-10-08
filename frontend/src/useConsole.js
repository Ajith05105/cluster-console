import { useEffect, useReducer } from "react";
import { initialState, reduce } from "./consoleState.js";

// The names of the messages the server sends on the live stream.
const MESSAGE_NAMES = ["hello", "history", "state", "stats", "warnings", "event"];

// useConsole opens the live stream (GET /api/stream) and returns the page's
// state, kept up to date as messages arrive.
//
// EventSource is the browser's built-in client for Server-Sent Events. If the
// connection drops it reconnects by itself; the server then re-sends the
// catch-up messages, so the page recovers without any extra code here.
export function useConsole() {
  const [state, dispatch] = useReducer(reduce, initialState);

  useEffect(() => {
    const stream = new EventSource("/api/stream");

    stream.onopen = () => dispatch({ type: "open" });
    stream.onerror = () => dispatch({ type: "closed" });

    for (const name of MESSAGE_NAMES) {
      stream.addEventListener(name, (event) => {
        dispatch({ type: name, data: JSON.parse(event.data) });
      });
    }

    // Close the stream when the page goes away.
    return () => stream.close();
  }, []);

  return state;
}

# frontend

The console's web page: a React single-page app, built with Vite. When the
console image is built, the page is built too and packed inside the Go
program (`backend/web/`), so there is nothing separate to deploy.

## What is on the page

| Part | File | What it shows or does |
|---|---|---|
| Header | `src/components/Header.jsx` | Connection state, Argo CD summary, the access-token box |
| Warning banner | `src/components/WarningBanner.jsx` | The console's current warnings, each with its reason |
| Nodes | `src/components/Nodes.jsx` | A card per worker with a square per pod, and the "Waiting for a node" box for Pending pods |
| Charts | `src/components/Charts.jsx` | Frames per second (demand, processed, failed) and pods (wanted, ready, waiting), sharing one time axis |
| Controls | `src/components/Controls.jsx` | Deploy/Remove, the replica stepper, camera load, marks, delete-a-pod, CSV downloads |
| Event log | `src/components/EventLog.jsx` | What has happened, newest first, filterable by kind |

`src/App.jsx` lays these out and holds the three things they share: the live
state, the access token, and which pod is selected.

## How the data gets there

- `src/useConsole.js` opens the live stream (`GET /api/stream`) and keeps the
  page's state up to date. The browser reconnects by itself if it drops.
- `src/consoleState.js` is the plain function that says how each stream
  message changes the state.
- `src/api.js` sends changes: every button is a `POST` with the access token.

The page never polls. Everything it shows arrives on the stream.

## The access token

Typed into the box at the top right. It is kept for the browser tab only
(sessionStorage) and sent with every change. Without an accepted token, or in
read-only mode, the page can be watched but the controls are switched off.
See `deploy/README.md` for how to read the token from the cluster.

## Colours

The five palette colours, plus lighter or see-through versions of them:

| Colour | Used for |
|---|---|
| Beige `#EEF4D4` | Page background |
| Tea Green `#DAEFB3` | Good: ready pods, the no-warnings strip, chart fills |
| Sweet Salmon `#EA9E8D` | Waiting: Pending pods, pods shutting down |
| Raspberry `#D64550` | Wrong: warnings, NotReady, failed frames, destructive buttons |
| Jet Black `#1C2826` | Text, chart lines, ordinary buttons |

Tea Green and Sweet Salmon are too pale to read as thin lines (measured
contrast against the card background is 1.2 and 2.0), so in the charts they
are only ever fills under a Jet Black line. Pod states differ in shape as
well as colour (solid, dashed, dotted, crossed) so they can be told apart
without relying on colour.

All times on the page are UTC, to match the CSV files.

## Working on it

Node is needed on the laptop for these; it is not needed in the cluster.

```
make test-page                                        # run the page's tests
make dev-page CONSOLE_URL=http://<gateway address>    # live-reload page, real data
```

`make dev-page` serves the page from the laptop and forwards `/api` to the
real console, so changes to the page show at once with live cluster data.

`make image-console` runs the tests and the build inside a container; that is
what produces the page that ships.

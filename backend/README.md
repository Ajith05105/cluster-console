# backend

Go service: the console API and the load generator. One program, built into
one container image (`backend/Dockerfile`), with the React page from
`frontend/` packed inside it.

## Where things are

| Path | What it does |
|---|---|
| `cmd/console/main.go` | Reads the settings and wires the parts together |
| `api/` | The web API, the live stream, and the once-a-second loop |
| `cluster/` | Watches Kubernetes and carries out actions, in `workload` only |
| `loadgen/` | The virtual cameras |
| `warnings/` | The five rules behind the warning banner |
| `record/` | The event log and per-second stats, and their CSV files |
| `catalog/` | What may be deployed, and the templates it is deployed from |
| `web/` | Serves the React page. `web/dist/` holds a placeholder here; the image build replaces it with the real page |
| `cmd/render-workload/` | Prints the YAML Deploy would create (`make render`) |

Every Go file has plain-English comments. Start with `cmd/console/main.go`.

## The API

Reading needs no token. Every `POST` needs the header
`Authorization: Bearer <token>`, and is refused in read-only mode or if no
token is configured.

| Request | Body | What it does |
|---|---|---|
| `GET /api/state` | | Snapshot: nodes with their pods, Pending pods and why, the workload, the autoscaler, Argo CD apps, load status, warnings |
| `GET /api/catalog` | | What can be deployed, the limits, whether changes are off |
| `GET /api/stream` | | Live stream (Server-Sent Events), see below |
| `GET /api/files` | | List of CSV files |
| `GET /api/files/{name}` | | Download one CSV file |
| `POST /api/deploy` | `{"image_key", "cpu_request", "memory_request", "max_replicas", "fast_node_death"}` | Deploys a catalog entry, replacing whatever the console deployed before. Only `image_key` is required |
| `POST /api/undeploy` | | Removes what the console deployed |
| `POST /api/scale` | `{"replicas"}` | Sets the pod count to at least this (see note) |
| `POST /api/pod/delete` | `{"name"}` | Deletes one pod in `workload` |
| `POST /api/load/start` | `{"cameras", "fps"}` | Starts the load, or changes it |
| `POST /api/load/cameras` | `{"cameras"}` | Changes the camera count while running |
| `POST /api/load/stop` | | Stops the load |
| `POST /api/mark` | `{"label"}` | Adds a named moment to the event log |
| `POST /api/auth/check` | | Does nothing; lets the page test a token |

No request has a namespace field, and unknown fields are refused.

**Scale is a floor, not a fixed number.** The autoscaler also controls the
pod count and would undo a plain change within a minute. So `scale` sets the
autoscaler's minimum to the number asked for and sets the Deployment to it
straight away. The autoscaler can still go higher under load.

### Live stream messages

| Name | When | Contents |
|---|---|---|
| `hello` | on connecting | catalog, limits, read-only flag |
| `history` | on connecting | recent stats rows and events |
| `state` | when the cluster changes | same as `GET /api/state` |
| `stats` | every second | one stats row |
| `warnings` | when there are any, and when they clear | the full list |
| `event` | as it happens | one event-log line |

## Warnings

Each carries a reason sentence.

| Rule | Raised when |
|---|---|
| Pending pods | a pod has been unschedulable for more than 15 s (reason includes the scheduler's, e.g. `Insufficient cpu`) |
| Node | any node is NotReady, worker or control-plane |
| Replicas | ready pods have been below the wanted number for more than 10 s |
| Frames | more than 1% of frames failed over the last 10 s (needs at least 20 frames sent) |
| Argo CD | an application is OutOfSync |

## CSV files

Written to `DATA_DIR` (a volume in the cluster), a new pair each time the
console starts. Timestamps are UTC, like `2026-10-08T06:30:27.001Z`.

`stats-<start>.csv`, one row per second **while a workload is deployed or
load is running**. An idle console writes no stats rows, to spare the disk it
runs on, so the file has gaps wherever nothing was deployed. The event log
records when recording starts and stops. (The page's charts are fed from
memory and keep updating every second regardless.)

```
time_utc,cameras,fps,demand_fps,sent,processed,failed,failed_busy,failed_timeout,failed_other,
latency_mean_ms,latency_p95_ms,desired_pods,ready_pods,pending_pods,ready_nodes,per_node
```

`per_node` is the processed frames by node, e.g. `agent-10=25;agent-7=25`.

`events-<start>.csv`: `time_utc,kind,message`. Kinds are `mark`, `action`,
`cluster`, `load` and `warning`.

## Settings (environment variables)

| Name | Default | Meaning |
|---|---|---|
| `CONSOLE_TOKEN` | (empty) | Access token for POSTs, from a Secret. Empty means all POSTs are refused |
| `READ_ONLY` | `false` | `true` refuses all POSTs even with the token |
| `LISTEN_ADDR` | `:8080` | Address to serve on |
| `DATA_DIR` | `/data` | Where the CSV files go |
| `LOAD_TARGET_URL` | `http://traefik.kube-system.svc.cluster.local` | Where frames are sent |
| `LOAD_HOST` | `workload.cluster.local` | Host header on every frame |
| `LOAD_MAX_RATE` | `600` | Highest cameras x fps allowed |
| `LOAD_TIMEOUT_MS` | `2000` | How long to wait for each reply |
| `ARGOCD_NAMESPACE` | `argocd` | Where Argo CD's applications are read from |

## Safety tests

`make test` includes two that together show nothing outside `workload` can be
scaled or deleted:

- `api/security_test.go` fills a fake Kubernetes with look-alike objects in
  other namespaces, uses every POST both normally and with hostile input, and
  checks that no changing call was aimed outside `workload` and nothing else
  was altered.
- `api/rbac_manifest_test.go` checks `deploy/manifests/console-rbac.yaml`
  against the agreed permissions, including no access to ResourceQuotas.

`scripts/check-rbac.sh` asks the real cluster the same questions once the
permissions are applied.

## Adding a workload to the dropdown

Add an entry to `catalog/catalog.json`, run `make mirror` to copy its image
into Gitea, then `make test`.

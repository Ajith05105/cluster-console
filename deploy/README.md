# deploy

Kubernetes manifests and the Argo CD Application.

| Path | What it is |
|---|---|
| `argocd/cluster-console.yaml` | The one Argo CD Application for this project. It lives in the `argocd` namespace, so the owner applies it by hand |
| `manifests/` | Everything that Application applies: the ResourceQuota in `workload` now, and the console itself in `console` once it is written |

## Who creates what

| Object | Namespace | Created by |
|---|---|---|
| ResourceQuota `workload-quota` | `workload` | Argo CD, from `manifests/workload-quota.yaml` |
| The console (Deployment, Service, RBAC, route) | `console` | Argo CD, from `manifests/` (later phases) |
| The workload's Deployment, Service, autoscaler and Traefik route | `workload` | The console's **Deploy** button; deleted by **Remove** |

The workload objects are not in this folder. They are templates in
`backend/catalog/templates/`, filled in from `backend/catalog/catalog.json`
and the Deploy form. Every object made from them carries the label
`managed-by=console`, and Remove only deletes objects with that label.

To see exactly what Deploy would create, without touching the cluster:

```
make render KEY=camera-ingest
```

The same output piped into `kubectl apply -f -` creates the workload by hand,
which is the fallback if the console is unavailable during a demo.

## Rules the console's permissions must follow

- The console must **not** be able to create, edit or delete ResourceQuotas.
  The quota is the safety ceiling on the console itself, so only Argo CD and
  the owner may change it.
- Argo CD has `selfHeal` off for this Application: it applies new commits but
  does not undo changes made on the cluster.

## Where workload pods are allowed to run

Pods use node affinity with two rules: the node must not be a control-plane
node, and must not be `mac-mini-agent`. No node labels are needed, so a
worker that joins later is eligible straight away.

## How quickly pods on a dead node are replaced

This is switchable per deploy:

| Setting | Wait before replacing pods | Notes |
|---|---|---|
| baseline (default) | 300 seconds | Plain Kubernetes behaviour |
| fast | 20 seconds | For showing quick recovery |

The two values are `node_death_seconds` in `backend/catalog/catalog.json`.
`make render KEY=camera-ingest ARGS=-fast` shows the fast version.

## How the load generator reaches the workload

The route's host name is `workload.cluster.local`. That name does **not**
resolve inside the cluster (checked from a pod on server-1: NXDOMAIN), so the
load generator connects to Traefik's in-cluster Service,
`traefik.kube-system.svc.cluster.local`, and sends
`Host: workload.cluster.local` with every request. Nothing needs adding to
the cluster for this.

## How the CPU request was chosen

The CPU request decides how many pods fit on a worker. The target was 6 to 8
pods per worker.

Measured on the cluster on 2026-10-08 at 04:28 UTC:

| Worker | Allocatable CPU | Already requested by other pods | Free |
|---|---|---|---|
| agent-7 | 4000m | 75m (node-exporter 25m, one whoami pod 50m) | 3925m |
| agent-10 | 4000m | 25m (node-exporter 25m) | 3975m |

Pods that fit = free CPU divided by the request, rounded down:

| CPU request | agent-7 | agent-10 |
|---|---|---|
| 450m | 8 | 8 |
| **500m** | **7** | **7** |
| 550m | 7 | 7 |
| 600m | 6 | 6 |

500m was chosen: 7 pods per worker, 14 across the two workers.

Checked on the cluster on 2026-10-08 at 04:57 UTC by asking for 20 pods:

| Where | Pods |
|---|---|
| agent-7 | 7 running |
| agent-10 | 7 running |
| Pending (no node) | 6, scheduler reason `Insufficient cpu` on both workers |

So at full scale-out, 6 pods wait in Pending until another worker joins.

Memory is not the limit. Seven pods request 7 x 128Mi = 896Mi and can use at
most 7 x 256Mi = 1792Mi, against 3736Mi free on agent-7, the smaller worker.

## Other choices worth knowing about

- **CPU limit of 1 core per camera-ingest pod.** Makes one pod's capacity
  predictable: with `WORK_MS=40` a pod can process at most 25 frames a second.
- **Scale-down waits 60 seconds** of low load, not the default 5 minutes.
- **The route is a plain Ingress**, like the four routes already on the
  cluster.

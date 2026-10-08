# cluster-console

A test and demo console for a self-provisioning, self-healing k3s micro data
centre built on Raspberry Pis. It gives one place to run a demo workload on
the cluster, put load on it, trigger and watch events such as a node joining
or dropping out, and record what was measured, so the cluster's behaviour can
be shown live and backed with data.

**Status: scaffold only.** This repository contains the folder layout and
project rules. No application code has been written yet.

## Folder map

| Folder | What goes here |
|---|---|
| `backend/` | Go service: the console API and the load generator |
| `camera-ingest/` | Go service: the demo workload |
| `frontend/` | React single-page app, built and embedded into the Go binary later |
| `deploy/` | Kubernetes manifests and Argo CD Applications |
| `docs/` | `HOW-IT-WORKS.md` and the dry-run checklist |
| `data/` | CSV output from test runs; ignored by git except `.gitkeep` |

## Working rules

The rules for working in this repo, including what may and may not be touched
on the cluster, are in [CLAUDE.md](CLAUDE.md).

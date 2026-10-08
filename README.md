# cluster-console

A test and demo console for a self-provisioning, self-healing k3s micro data
centre built on Raspberry Pis. It gives one place to run a demo workload on
the cluster, put load on it, trigger and watch events such as a node joining
or dropping out, and record what was measured, so the cluster's behaviour can
be shown live and backed with data.

**Status: Phase 3 done.** The demo workload (`camera-ingest`), the image
catalog, the console's backend (API, live stream, load generator, warnings,
CSV recording) and the React page are written and run in the cluster as one
container. The how-it-works document and the demo checklist are still to come.

## Folder map

| Folder | What goes here |
|---|---|
| `backend/` | Go service: the console API and the load generator. `backend/catalog/` holds the list of images the console may deploy and the templates it deploys them from |
| `camera-ingest/` | Go service: the demo workload |
| `frontend/` | React single-page app; built and packed inside the Go program when the console image is built |
| `deploy/` | The Argo CD Application and the manifests it applies |
| `docs/` | `HOW-IT-WORKS.md` and the dry-run checklist |
| `data/` | CSV output from test runs; ignored by git except `.gitkeep` |
| `internal/scene/` | Go code that draws the fake camera pictures, shared by `camera-ingest` and the load generator |
| `scripts/` | `mirror-images.sh` copies the catalog's public images into the Gitea registry; `check-rbac.sh` asks the cluster what the console is allowed to do |

`make test` runs the Go checks inside a container; see the `Makefile` for the
other shortcuts.

## Working rules

The rules for working in this repo, including what may and may not be touched
on the cluster, are in [CLAUDE.md](CLAUDE.md).

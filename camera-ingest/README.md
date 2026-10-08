# camera-ingest

Go service: the demo workload. It pretends to process frames from security
cameras, and costs a fixed amount of CPU per frame so that load on the
cluster is visible.

## What it answers

| Request | What happens |
|---|---|
| `POST /frame` with a JPEG body | Decodes the picture, shrinks it to 64x48 grey, compares it with a fixed empty-room picture to decide "motion or not", then keeps the CPU busy until the frame has cost `WORK_MS` of CPU time. Replies with small JSON |
| `GET /ready` | 200 when it can take traffic, 503 while shutting down |
| `GET /healthz` | 200 while the program is alive |

Every `/frame` reply carries an `X-Node` header (and `node` in the JSON) with
the name of the node the pod runs on. The load generator counts frames per
node from that header.

Replies to `/frame`:

- `200` processed
- `400` body is not a JPEG
- `413` body over 1 MiB, or picture over 1920x1080
- `503` busy: the pod is already working on `MAX_INFLIGHT` frames

It keeps nothing between requests, so any pod can take any frame.

## Settings (environment variables)

| Name | Default | Meaning |
|---|---|---|
| `PORT` | 8080 | Port to listen on |
| `WORK_MS` | 40 | CPU milliseconds each frame costs |
| `MAX_INFLIGHT` | 8 | Frames worked on at once before answering 503 |
| `NODE_NAME` | (empty) | Set by Kubernetes to the node's name |
| `POD_NAME` | (empty) | Set by Kubernetes to the pod's name |

## Files

| File | What it is |
|---|---|
| `main.go` | The whole service |
| `cputime_linux.go` | Asks Linux how much CPU the current thread has used |
| `cputime_other.go` | Rough stand-in for non-Linux machines |
| `main_test.go` | Automated tests |
| `Dockerfile` | Builds the container image |
| `../internal/scene/` | Draws the fake camera pictures (shared with the load generator) |

## Build and test

From the repository root, with the podman machine running:

```
make test
make image-camera-ingest
make push-camera-ingest
```

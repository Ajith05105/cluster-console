# backend

Go service: the console API and the load generator.

The API and load generator are not written yet. What exists so far is the
part that decides what the Deploy button may create:

| Path | What it is |
|---|---|
| `catalog/catalog.json` | The fixed list of workloads the console may deploy, with each one's image, port, health checks and default sizes |
| `catalog/templates/` | Templates for the four objects a workload is made of: Deployment, Service, autoscaler and Traefik route |
| `catalog/catalog.go` | Reads and checks `catalog.json` |
| `catalog/render.go` | Fills in the templates for one catalog entry |
| `catalog/render_test.go` | Automated tests for the two files above |
| `cmd/render-workload/` | Small command that prints the filled-in YAML; run it with `make render` |

To add a workload to the dropdown: add an entry to `catalog.json`, run
`make mirror` to copy its image into Gitea, then `make test`.

# Shortcuts for building and testing. Go is not installed on the laptop, so
# everything Go-related runs inside a container (needs `podman machine start`).
#
#   make test                  run the Go checks and tests
#   make render KEY=<key>      print the YAML the Deploy button would create
#   make fmt                   put the Go files into standard layout
#   make image-camera-ingest   build the camera-ingest image for arm64 + amd64
#   make push-camera-ingest    push that image to the Gitea registry
#   make image-console         build the console image for arm64 + amd64
#   make push-console          push that image to the Gitea registry
#   make mirror                copy the catalog's public images into Gitea
#
# The two push/mirror targets need you to have logged in first:
#   skopeo login --tls-verify=false gitea.cluster.local

GO_IMAGE ?= docker.io/library/golang:1.27.1-alpine
# The registry as reached from the laptop (through Traefik). Inside the
# cluster the same registry is gitea.cluster.local:3000.
REGISTRY ?= gitea.cluster.local
CAMERA_INGEST_VERSION ?= 0.1.0

# Must match the image tag in deploy/manifests/console.yaml.
CONSOLE_VERSION ?= 0.2.1

CAMERA_INGEST_LOCAL := localhost/camera-ingest:$(CAMERA_INGEST_VERSION)
CONSOLE_LOCAL       := localhost/cluster-console:$(CONSOLE_VERSION)
# Full paths, because podman resolves paths inside its own virtual machine.
CAMERA_INGEST_OCI   := $(CURDIR)/.build/camera-ingest-oci
CONSOLE_OCI         := $(CURDIR)/.build/cluster-console-oci

.PHONY: test fmt render image-camera-ingest push-camera-ingest image-console push-console mirror

# Rewrites the Go files into the standard layout. Only spacing changes.
fmt:
	podman run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) gofmt -l -w .

# Print the Kubernetes YAML the console's Deploy button would create for one
# catalog entry. Nothing is sent to the cluster. Examples:
#   make render KEY=camera-ingest
#   make render KEY=camera-ingest ARGS="-fast -cpu 250m -max 10"
#   make render ARGS=-list
KEY ?= camera-ingest
ARGS ?=
render:
	@podman run --rm -v "$(CURDIR)":/src:ro -w /src \
		-v cluster-console-gomod:/go/pkg/mod \
		-v cluster-console-gocache:/root/.cache/go-build \
		$(GO_IMAGE) go run ./backend/cmd/render-workload -key $(KEY) $(ARGS)

# The repo is mounted read-only, so the checks cannot change any file.
# gofmt -l prints files that are not in standard Go layout; any output fails.
test:
	podman run --rm -v "$(CURDIR)":/src:ro -w /src \
		-v cluster-console-gomod:/go/pkg/mod \
		-v cluster-console-gocache:/root/.cache/go-build \
		$(GO_IMAGE) sh -c 'test -z "$$(gofmt -l .)" || { echo "not gofmt-formatted:"; gofmt -l .; exit 1; }; go vet ./... && go test ./...'

image-camera-ingest:
	podman build --platform linux/arm64,linux/amd64 \
		--manifest $(CAMERA_INGEST_LOCAL) \
		-f camera-ingest/Dockerfile .

# Two steps: podman writes the multi-arch image to a folder, then skopeo
# (which runs on the laptop itself and so can reach Gitea by name) uploads it.
# The first step runs inside the podman virtual machine (`podman machine ssh`)
# because the Mac's podman client can only write to registries, not folders.
push-camera-ingest:
	rm -rf $(CAMERA_INGEST_OCI)
	mkdir -p $(CURDIR)/.build
	podman machine ssh -- podman manifest push --all $(CAMERA_INGEST_LOCAL) oci:$(CAMERA_INGEST_OCI)
	skopeo copy --all --dest-tls-verify=false \
		oci:$(CAMERA_INGEST_OCI) \
		docker://$(REGISTRY)/apps/camera-ingest:$(CAMERA_INGEST_VERSION)

image-console:
	podman build --platform linux/arm64,linux/amd64 \
		--manifest $(CONSOLE_LOCAL) \
		-f backend/Dockerfile .

push-console:
	rm -rf $(CONSOLE_OCI)
	mkdir -p $(CURDIR)/.build
	podman machine ssh -- podman manifest push --all $(CONSOLE_LOCAL) oci:$(CONSOLE_OCI)
	skopeo copy --all --dest-tls-verify=false \
		oci:$(CONSOLE_OCI) \
		docker://$(REGISTRY)/apps/cluster-console:$(CONSOLE_VERSION)

mirror:
	REGISTRY=$(REGISTRY) scripts/mirror-images.sh

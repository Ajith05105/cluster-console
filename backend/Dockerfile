# Builds the console container image.
# Build from the repository root (see `make image-console`).
#
# For now the image holds the Go backend only. The React page is added to
# this build in Phase 3 and embedded into the same program.

# --- Stage 1: compile -------------------------------------------------------
# Runs on the build machine's own processor type and cross-compiles for the
# target, so building for arm64 and amd64 needs no emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.1-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

# Download the libraries first, on their own. This step is only repeated when
# go.mod or go.sum change, which keeps rebuilds quick.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY internal ./internal
COPY backend ./backend
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/console ./backend/cmd/console

# --- Stage 2: the image that actually runs ----------------------------------
# An empty image holding just the one program, run as an unprivileged user.
FROM scratch
COPY --from=build /out/console /console
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/console"]

# Builds the camera-ingest container image.
# Build from the repository root (see `make image-camera-ingest`), because the
# code needs go.mod and internal/ as well as this folder.

# --- Stage 1: compile -------------------------------------------------------
# This stage always runs on the build machine's own processor type
# (BUILDPLATFORM) and cross-compiles for the target (TARGETARCH). Go can do
# that natively, so building the arm64 and amd64 images needs no emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.1-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
COPY internal ./internal
COPY camera-ingest ./camera-ingest
# CGO_ENABLED=0 makes one self-contained program file with no system
# libraries needed, which is what lets the final image be empty.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/camera-ingest ./camera-ingest

# --- Stage 2: the image that actually runs ----------------------------------
# "scratch" is an empty image: no shell, no tools, just our one program.
FROM scratch
COPY --from=build /out/camera-ingest /camera-ingest
# Run as an unprivileged user (65534 is the conventional "nobody").
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/camera-ingest"]

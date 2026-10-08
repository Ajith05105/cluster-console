# Builds the console container image.
# Build from the repository root (see `make image-console`).
#
# The image holds one program: the Go backend with the React page packed
# inside it. So the build has three stages: build the page, compile the
# program with the page inside, and copy the program into an empty image.

# --- Stage 1: build the React page -------------------------------------------
# Node is only needed here, at build time. It is not in the final image.
FROM --platform=$BUILDPLATFORM docker.io/library/node:24.21.0-alpine AS page
WORKDIR /page
# Install the libraries first, on their own, so this step is only repeated
# when the list of libraries changes. `npm ci` installs exactly the versions
# recorded in package-lock.json.
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY frontend/ ./
# Run the page's tests, then build it into /page/dist.
RUN npm test && npm run build

# --- Stage 2: compile the program --------------------------------------------
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
# Replace the placeholder page with the real one built in stage 1. The Go
# compiler packs whatever is in this folder into the program.
RUN rm -rf ./backend/web/dist
COPY --from=page /page/dist ./backend/web/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/console ./backend/cmd/console

# --- Stage 3: the image that actually runs ----------------------------------
# An empty image holding just the one program, run as an unprivileged user.
FROM scratch
COPY --from=build /out/console /console
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/console"]

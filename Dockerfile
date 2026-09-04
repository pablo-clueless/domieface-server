# Build stage. Pinned to the toolchain in go.mod: golang.org/x/crypto requires
# Go 1.26 or newer, so an older base image cannot compile this.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies are copied and downloaded first so the module cache layer is
# reused whenever only application code changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Reported by /healthz, so an operator can tell which build is serving traffic.
ARG VERSION=dev

# CGO_ENABLED=0 produces a static binary that runs on a distroless base.
# -trimpath keeps build paths out of the binary; -s -w drop the symbol table.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath     -ldflags="-s -w -X domieface/com/internal/buildinfo.version=${VERSION}"     -o /out/domieface-server .

# Runtime stage. Distroless ships CA certificates (needed to reach S3 over TLS)
# and nothing else — no shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/domieface-server /domieface-server

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/domieface-server"]

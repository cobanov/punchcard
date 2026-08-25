# syntax=docker/dockerfile:1

# --- build stage ---
FROM golang:1.26.6-alpine AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
# Static (CGO-free) binary with the migrations + (later) web UI embedded.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/punchcard ./cmd/punchcard

# --- runtime stage ---
# distroless static ships CA certificates (needed for outbound webhook TLS in
# Phase 3) and runs as a non-root user. No shell, minimal attack surface.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/punchcard /punchcard
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/punchcard"]
CMD ["serve"]

# syntax=docker/dockerfile:1

# Build stage. The image is built for the host platform (linux/arm64 on Apple silicon).
FROM golang:1.27.3 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/profile-service ./cmd/profile-service

# Runtime stage: static distroless, no shell, non-root.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/profile-service /profile-service
# Numeric UID/GID (distroless "nonroot") so Kubernetes runAsNonRoot can verify it.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/profile-service"]

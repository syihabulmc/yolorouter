# Multi-stage build for the Yolorouter image: frontend (node) -> backend
# (go, cross-compiled) -> a small alpine runtime. Both build stages run on
# the build host's native architecture (--platform=$BUILDPLATFORM) and the
# Go stage cross-compiles via TARGETOS/TARGETARCH, so a multi-arch
# `docker buildx build --platform linux/amd64,linux/arm64` needs no QEMU
# emulation — the final stage contains no RUN instructions at all. Its CA
# certificates are copied out of the build stage (the binary talks HTTPS to
# upstream providers); timezone data needs no copy, the binary embeds the
# IANA database via time/tzdata.

FROM --platform=$BUILDPLATFORM node:22.12-alpine AS frontend
WORKDIR /src/frontend
# Install dependencies before copying the source so code-only changes reuse
# the npm layer cache.
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# web/dist is gitignored and empty in a fresh checkout; the embed build tag
# requires a real frontend build there.
COPY --from=frontend /src/frontend/dist ./web/dist
ARG TARGETOS
ARG TARGETARCH
# VERSION must be the release tag with its leading v (matching GitHub's
# tag_name) for the update check's semver comparison; the "dev" default
# keeps plain local `docker build .` working, with update checks off.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILDTIME=unknown
ARG DEFAULT_GITHUB_REPO=
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags "release embed" -trimpath \
    -ldflags "-s -w \
      -X github.com/yolorouter/yolorouter/internal/version.Version=$VERSION \
      -X github.com/yolorouter/yolorouter/internal/version.Commit=$COMMIT \
      -X github.com/yolorouter/yolorouter/internal/version.BuildTime=$BUILDTIME \
      -X github.com/yolorouter/yolorouter/internal/version.DefaultGitHubRepo=$DEFAULT_GITHUB_REPO" \
    -o /out/yolorouter ./cmd/yolorouter

FROM alpine:3.22
LABEL org.opencontainers.image.source="https://github.com/yolorouter/yolorouter" \
      org.opencontainers.image.description="Self-hosted LLM gateway: four wire protocols in, any provider out, with failover, key rotation, and an embedded admin console." \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/yolorouter /usr/local/bin/yolorouter
# Marks the process as running inside a container: version checks keep
# working, but in-place binary replacement is refused — the image is
# immutable, upgrades happen by pulling a newer image.
ENV YOLOROUTER_IN_DOCKER=1
# All mutable state lives under the working directory: the first run
# generates configs/config.yaml (including the encryption master key) and
# data/yolorouter.db beneath it, so mounting a single volume here persists
# everything. Runs as root so a host bind mount (-v ./yolorouter:/yolorouter)
# works without ownership fixups.
WORKDIR /yolorouter
EXPOSE 8080
# busybox wget ships with the base image; /healthz is unauthenticated.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["yolorouter"]
CMD ["serve"]

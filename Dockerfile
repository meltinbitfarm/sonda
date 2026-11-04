# syntax=docker/dockerfile:1
#
# Two images from one file (D2, D5):
#   target "app"        (default) sonda on distroless static, nonroot, no shell
#   target "litestream" Litestream's scratch image + /data owned by nonroot + litestream.yml
# Both run as uid/gid 65532 and both ship an empty /data owned by 65532: whichever
# container mounts the empty named volume first, Docker copies that ownership onto it,
# so app, restore and litestream can all write /data without a root init step.
#
#   docker build -t sonda .                                   # app
#   docker build --target litestream -t sonda-litestream .    # restore / replicate
#   docker buildx build --platform linux/amd64 ...            # x86 server from an arm Mac

ARG GO_VERSION=1.27
# >= 0.5.6 (A1): restore -if-db-not-exists -if-replica-exists, and replicate
# -restore-if-db-not-exists for the single-container fallback of Q1.
ARG LITESTREAM_VERSION=0.5.17

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags="-s -w -X github.com/meltinbitfarm/sonda/internal/cli.Version=${VERSION}" \
      -o /out/sonda ./cmd/sonda
RUN mkdir -p /out/data

FROM litestream/litestream:${LITESTREAM_VERSION}-scratch AS litestream
COPY --from=build --chown=65532:65532 /out/data /data
COPY deploy/litestream.yml /etc/litestream.yml
USER 65532:65532
# ENTRYPOINT is litestream; the command (restore / replicate) comes from compose.

FROM gcr.io/distroless/static-debian13:nonroot AS app
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/sonda /sonda
USER 65532:65532
ENV SONDA_ADDR=:8080 \
    SONDA_DB_PATH=/data/sonda.db
EXPOSE 8080
# No shell or curl in the image: the binary checks itself (GET /healthz over loopback).
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --start-interval=2s --retries=3 \
    CMD ["/sonda", "healthcheck"]
ENTRYPOINT ["/sonda"]
CMD ["serve"]

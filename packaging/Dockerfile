# Override GO_IMAGE with an official tag or digest when deliberately updating Go.
ARG GO_IMAGE=golang:1.26.10-bookworm
FROM ${GO_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc-x86-64-linux-gnu libc6-dev-amd64-cross \
    gcc-aarch64-linux-gnu libc6-dev-arm64-cross \
    gcc-mingw-w64-x86-64 \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /cache/build /cache/mod \
    && chmod 777 /cache /cache/build /cache/mod
ENV CGO_ENABLED=1 GOTOOLCHAIN=local GOCACHE=/cache/build GOMODCACHE=/cache/mod
WORKDIR /src

#!/bin/sh
# Native Go c-shared builds. Docker only sees source read-only and dist writable.
set -eu
cd "$(dirname "$0")/.."
root=$(pwd -P)
target=${1:-host}
go_cmd=${GO:-go}
version=${VERSION:-0.1.0}
case "$version" in [0-9]*) ;; *) echo 'VERSION must start with a digit' >&2; exit 2;; esac
case "$version" in *[!0-9A-Za-z.+-]*) echo 'Invalid VERSION' >&2; exit 2;; esac
export GOTOOLCHAIN=${GOTOOLCHAIN:-go1.26.10}
export GOCACHE=${GOCACHE:-${TMPDIR:-/tmp}/cpa-model-dock-go-cache}
builder=${DOCKER_BUILDER_IMAGE:-cpa-model-dock-builder:go1.26.10-bookworm}
go_image=${GO_IMAGE:-golang:1.26.10-bookworm}

docker_builder() {
    docker build --build-arg "GO_IMAGE=$go_image" -t "$builder" packaging
}
if [ "$target" = docker-builder ]; then docker_builder; exit; fi

host_os=$("$go_cmd" env GOHOSTOS)
host_arch=$("$go_cmd" env GOHOSTARCH)
if [ "$target" = host ]; then
    target_os=$host_os; target_arch=$host_arch
    output="$root/dist"
else
    target_os=${target%/*}; target_arch=${target#*/}
    output="$root/dist/$target_os/$target_arch"
fi
case "$target_os/$target_arch" in
    darwin/amd64|darwin/arm64) ext=dylib;;
    linux/amd64|linux/arm64) ext=so;;
    windows/amd64) ext=dll;;
    *) echo "Unsupported target: $target_os/$target_arch" >&2; exit 2;;
esac
mkdir -p "$output"

if [ "$target" != host ] && [ "$target_os" != darwin ]; then
    if ! docker image inspect "$builder" >/dev/null 2>&1; then docker_builder; fi
    case "$target_os/$target_arch" in
        linux/amd64) compiler=x86_64-linux-gnu-gcc;;
        linux/arm64) compiler=aarch64-linux-gnu-gcc;;
        windows/amd64) compiler=x86_64-w64-mingw32-gcc;;
    esac
    # Named volumes avoid changing host module/build caches or any source files.
    docker run --rm --user "$(id -u):$(id -g)" \
        --mount "type=bind,src=$root,dst=/src,readonly" \
        --mount "type=bind,src=$output,dst=/out" \
        --mount type=volume,src=cpa-model-dock-go-cache,dst=/cache \
        -e "GOOS=$target_os" -e "GOARCH=$target_arch" -e "CC=$compiler" \
        -e "VERSION=$version" -e "EXT=$ext" -e "GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}" \
        "$builder" sh -ec '
            temp=$(mktemp -d /out/.build.XXXXXX)
            trap '\''rm -rf "$temp"'\'' EXIT HUP INT TERM
            go build -mod=readonly -trimpath -buildmode=c-shared \
                -ldflags "-s -w -X main.pluginVersion=$VERSION" \
                -o "$temp/cpa-model-dock.$EXT" .
            mv "$temp/cpa-model-dock.$EXT" /out/
        '
else
    export CGO_ENABLED=1 GOOS=$target_os GOARCH=$target_arch
    if [ "$target_os" = darwin ]; then
        if [ "$host_os" != darwin ]; then
            echo 'macOS builds require a macOS host with Apple Command Line Tools.' >&2
            exit 2
        fi
        export CC=/usr/bin/clang
        export MACOSX_DEPLOYMENT_TARGET=${MACOSX_DEPLOYMENT_TARGET:-12.0}
        case "$target_arch" in amd64) apple_arch=x86_64;; arm64) apple_arch=arm64;; esac
        export CGO_CFLAGS="${CGO_CFLAGS:-} -arch $apple_arch -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
        export CGO_LDFLAGS="${CGO_LDFLAGS:-} -arch $apple_arch -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
    else
        export CC=${CC:-cc}
    fi
    temp=$(mktemp -d "$output/.build.XXXXXX")
    trap 'rm -rf "$temp"' EXIT HUP INT TERM
    "$go_cmd" build -mod=readonly -trimpath -buildmode=c-shared \
        -ldflags "-s -w -X main.pluginVersion=$version" \
        -o "$temp/cpa-model-dock.$ext" .
    mv "$temp/cpa-model-dock.$ext" "$output/"
fi
ls -l "$output/cpa-model-dock.$ext"
file "$output/cpa-model-dock.$ext"

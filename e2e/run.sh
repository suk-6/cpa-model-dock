#!/usr/bin/env bash
set -euo pipefail
E2E_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$E2E_DIR/.." && pwd)"
export GOTOOLCHAIN=go1.26.10
export CC=/usr/bin/clang CGO_ENABLED=1
export GOPROXY="${GOPROXY:-off}"
export GOFLAGS="${GOFLAGS:--mod=readonly}"
export GOWORK=off
# Keep Go build-cache writes inside a sandbox-writable location.
export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/cpa-model-dock-go-build}"
export CPA_MODEL_DOCK_LIB="${CPA_MODEL_DOCK_LIB:-$REPO_ROOT/../dist/cpa-model-dock.dylib}"
export CPA_SOURCE="${CPA_SOURCE:-/Users/woosuk/git-files/cpa-model-dock/.ref/CLIProxyAPI}"
export E2E_WORK_DIR
E2E_WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cpa-model-dock-e2e.XXXXXX")"
runner_pid=""
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [[ -n "$runner_pid" ]] && kill -0 "$runner_pid" 2>/dev/null; then
    kill -TERM "$runner_pid" 2>/dev/null || true
    wait "$runner_pid" || true
  fi
  if [[ "$result" != 0 || "${E2E_KEEP_TMP:-0}" == 1 ]]; then
    echo "Artifacts retained: $E2E_WORK_DIR" >&2
    if [[ "$result" != 0 && -f "$E2E_WORK_DIR/cpa.log" ]]; then
      tail -n 60 "$E2E_WORK_DIR/cpa.log" >&2
    fi
  else
    rm -rf -- "$E2E_WORK_DIR"
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ "${E2E_NO_PLUGIN:-0}" != 1 && ! -f "$CPA_MODEL_DOCK_LIB" ]]; then
  echo "FAIL plugin library missing: $CPA_MODEL_DOCK_LIB (set CPA_MODEL_DOCK_LIB, or E2E_NO_PLUGIN=1)" >&2
  exit 1
fi
if [[ -n "${CPA_BIN:-}" ]]; then
  CPA_BIN="$(cd -- "$(dirname -- "$CPA_BIN")" && pwd)/$(basename -- "$CPA_BIN")"
  [[ -x "$CPA_BIN" ]] || { echo "FAIL CPA_BIN is not executable: $CPA_BIN" >&2; exit 1; }
else
  export CPA_BIN="$E2E_WORK_DIR/cpa"
  echo "Building CPA with $GOTOOLCHAIN, CC=$CC, CGO_ENABLED=$CGO_ENABLED"
  (cd -- "$CPA_SOURCE" && go build -o "$CPA_BIN" ./cmd/server)
fi
export CPA_BIN
(cd -- "$E2E_DIR" && go build -o "$E2E_WORK_DIR/harness" .)
"$E2E_WORK_DIR/harness" &
runner_pid=$!
wait "$runner_pid"
runner_pid=""

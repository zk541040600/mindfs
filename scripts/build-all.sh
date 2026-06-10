#!/usr/bin/env bash
# Cross-compile mindfs for all target platforms.
# Usage: bash scripts/build-all.sh [VERSION] [DIST_DIR]
set -euo pipefail

VERSION="${1:-dev}"
DIST_DIR="${2:-dist}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.."; pwd)"

PLATFORMS=(
  "darwin/amd64"
  "darwin/arm64"
  "linux/amd64"
  "linux/arm64"
  "linux/arm"
  "windows/amd64"
  "windows/arm64"
)

echo "==> Building mindfs ${VERSION}"
echo "    Output: ${DIST_DIR}/"
echo

built_dirs=()
for PLATFORM in "${PLATFORMS[@]}"; do
  GOOS="${PLATFORM%%/*}"
  GOARCH="${PLATFORM##*/}"

  BIN_NAME="mindfs"
  [[ "$GOOS" == "windows" ]] && BIN_NAME="mindfs.exe"

  OUT_DIR="${DIST_DIR}/mindfs_${VERSION}_${GOOS}_${GOARCH}"
  rm -rf "${ROOT}/${OUT_DIR}"
  mkdir -p "${ROOT}/${OUT_DIR}"
  built_dirs+=("$(basename "${OUT_DIR}")")

  TARGET="${OUT_DIR}/${BIN_NAME}"
  printf "  %-20s -> %s\n" "${GOOS}/${GOARCH}" "${TARGET}"

  GOARM=""
  if [[ "$GOARCH" == "arm" ]]; then
    GOARM=7
  fi

  CGO_ENABLED=0 \
    GOOS="$GOOS" \
    GOARCH="$GOARCH" \
    GOARM="$GOARM" \
    go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o "${ROOT}/${TARGET}" \
      ./cli/cmd

  # Bundle web assets, default agent config, and Pi SDK bridge assets.
  if [[ -d "${ROOT}/web/dist" ]]; then
    cp -r "${ROOT}/web/dist" "${ROOT}/${OUT_DIR}/web"
  fi
  if [[ -f "${ROOT}/agents.json" ]]; then
    cp "${ROOT}/agents.json" "${ROOT}/${OUT_DIR}/agents.json"
  fi
  if [[ -f "${ROOT}/server/internal/agent/pi_sdk_bridge/probe.mjs" ]]; then
    BRIDGE_OUT="${ROOT}/${OUT_DIR}/server/internal/agent/pi_sdk_bridge"
    mkdir -p "${BRIDGE_OUT}"
    cp "${ROOT}/server/internal/agent/pi_sdk_bridge/probe.mjs" "${BRIDGE_OUT}/probe.mjs"
    if [[ -f "${ROOT}/server/internal/agent/pi_sdk_bridge/README.md" ]]; then
      cp "${ROOT}/server/internal/agent/pi_sdk_bridge/README.md" "${BRIDGE_OUT}/README.md"
    fi
  fi
done

# Produce archives
cd "${ROOT}/${DIST_DIR}"
for DIR in "${built_dirs[@]}"; do
  GOOS="$(echo "$DIR" | cut -d_ -f3)"
  if [[ "$GOOS" == "windows" ]]; then
    rm -f "${DIR}.zip"
    zip -qr "${DIR}.zip" "$DIR"
    echo "  Archived: ${DIST_DIR}/${DIR}.zip"
  else
    rm -f "${DIR}.tar.gz"
    tar czf "${DIR}.tar.gz" "$DIR"
    echo "  Archived: ${DIST_DIR}/${DIR}.tar.gz"
  fi
done

echo
echo "==> Done. Artifacts in ${DIST_DIR}/"

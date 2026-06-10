#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="mindfs.service"
ADDR="10.23.50.137:7331"

if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemctl not found; this script expects mindfs to be managed by systemd" >&2
  exit 1
fi

systemctl stop "${SERVICE_NAME}"

if command -v ss >/dev/null 2>&1 && ss -ltnp 2>/dev/null | grep -q "${ADDR}"; then
  echo "warning: ${ADDR} is still listening after stopping ${SERVICE_NAME}" >&2
  ss -ltnp | grep "${ADDR}" >&2 || true
  exit 1
fi

echo "${SERVICE_NAME} stopped"

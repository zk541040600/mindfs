#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="mindfs.service"
ADDR="10.23.50.137:7331"

systemctl --no-pager --full status "${SERVICE_NAME}" --lines=30 || true

echo
/root/mindfs/mindfs -addr "${ADDR}" -status || true

echo
if command -v ss >/dev/null 2>&1; then
  ss -ltnp | grep "${ADDR}" || true
fi

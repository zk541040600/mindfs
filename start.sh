#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="mindfs.service"

if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemctl not found; this script expects mindfs to be managed by systemd" >&2
  exit 1
fi

systemctl daemon-reload
systemctl start "${SERVICE_NAME}"
systemctl --no-pager --full status "${SERVICE_NAME}" --lines=20

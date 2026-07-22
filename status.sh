#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${MINDFS_COMPOSE_FILE:-${ROOT_DIR}/docker-compose.yml}"
COMPOSE_SERVICE="${MINDFS_COMPOSE_SERVICE:-mindfs}"
SERVICE_NAME="mindfs.service"
ADDR="10.23.50.137:7331"

if command -v systemctl >/dev/null 2>&1 &&
  [[ "$(systemctl show "${SERVICE_NAME}" -p LoadState --value 2>/dev/null || true)" == "loaded" ]]; then
  systemctl --no-pager --full status "${SERVICE_NAME}" --lines=30 || true
elif [[ -f "${COMPOSE_FILE}" ]] && command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  docker compose -f "${COMPOSE_FILE}" ps "${COMPOSE_SERVICE}" || true
elif [[ -f "${COMPOSE_FILE}" ]] && command -v docker-compose >/dev/null 2>&1; then
  docker-compose -f "${COMPOSE_FILE}" ps "${COMPOSE_SERVICE}" || true
fi

echo
/root/mindfs/mindfs -addr "${ADDR}" -status || true

echo
if command -v ss >/dev/null 2>&1; then
  ss -ltnp | grep "${ADDR}" || true
fi

#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${MINDFS_COMPOSE_FILE:-${ROOT_DIR}/docker-compose.yml}"
COMPOSE_SERVICE="${MINDFS_COMPOSE_SERVICE:-mindfs}"
SERVICE_NAME="mindfs.service"

if [[ -f "${COMPOSE_FILE}" ]] && command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  docker compose -f "${COMPOSE_FILE}" up -d --build "${COMPOSE_SERVICE}"
  docker compose -f "${COMPOSE_FILE}" ps "${COMPOSE_SERVICE}"
  exit 0
fi

if [[ -f "${COMPOSE_FILE}" ]] && command -v docker-compose >/dev/null 2>&1; then
  docker-compose -f "${COMPOSE_FILE}" up -d --build "${COMPOSE_SERVICE}"
  docker-compose -f "${COMPOSE_FILE}" ps "${COMPOSE_SERVICE}"
  exit 0
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload
  systemctl start "${SERVICE_NAME}"
  systemctl --no-pager --full status "${SERVICE_NAME}" --lines=20
  exit 0
fi

echo "docker compose/docker-compose and systemctl are not available" >&2
exit 1

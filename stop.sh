#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${MINDFS_COMPOSE_FILE:-${ROOT_DIR}/docker-compose.yml}"
COMPOSE_SERVICE="${MINDFS_COMPOSE_SERVICE:-mindfs}"
SERVICE_NAME="mindfs.service"
ADDR="10.23.50.137:7331"

if [[ -f "${COMPOSE_FILE}" ]] && command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  docker compose -f "${COMPOSE_FILE}" stop "${COMPOSE_SERVICE}"
  exit 0
fi

if [[ -f "${COMPOSE_FILE}" ]] && command -v docker-compose >/dev/null 2>&1; then
  docker-compose -f "${COMPOSE_FILE}" stop "${COMPOSE_SERVICE}"
  exit 0
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl stop "${SERVICE_NAME}"

  if command -v ss >/dev/null 2>&1 && ss -ltnp 2>/dev/null | grep -q "${ADDR}"; then
    echo "warning: ${ADDR} is still listening after stopping ${SERVICE_NAME}" >&2
    ss -ltnp | grep "${ADDR}" >&2 || true
    exit 1
  fi

  echo "${SERVICE_NAME} stopped"
  exit 0
fi

echo "docker compose/docker-compose and systemctl are not available" >&2
exit 1

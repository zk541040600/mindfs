#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${MINDFS_BIN:-${ROOT_DIR}/mindfs}"
ADDR="${MINDFS_ADDR:-10.23.50.137:7331}"
SERVICE_NAME="${MINDFS_SERVICE_NAME:-mindfs.service}"
COMPOSE_FILE="${MINDFS_COMPOSE_FILE:-/root/mindfs/docker-compose.yml}"
COMPOSE_SERVICE="${MINDFS_COMPOSE_SERVICE:-mindfs}"

sanitize_addr() {
  local input="${1:-}" out="" ch
  if [[ -z "${input}" ]]; then
    printf 'default'
    return
  fi
  for ((i = 0; i < ${#input}; i++)); do
    ch="${input:i:1}"
    if [[ "${ch}" =~ [A-Za-z0-9] ]]; then
      out+="${ch}"
    else
      out+="_"
    fi
  done
  printf '%s' "${out:-default}"
}

process_exists() {
  local pid="${1:-}"
  [[ -n "${pid}" && -d "/proc/${pid}" ]]
}

is_mindfs_pid() {
  local pid="${1:-}" exe="" cmdline=""
  [[ -n "${pid}" && -r "/proc/${pid}/cmdline" ]] || return 1
  exe="$(readlink "/proc/${pid}/exe" 2>/dev/null || true)"
  cmdline="$(tr '\0' ' ' <"/proc/${pid}/cmdline" 2>/dev/null || true)"
  [[ "${exe}" == "${BIN}"* || "${cmdline}" == "${BIN} "* ]]
}

pid_file() {
  local key
  key="$(sanitize_addr "${ADDR}")"
  printf '%s/.local/share/mindfs/mindfs-%s.pid' "${HOME}" "${key}"
}

find_mindfs_pid() {
  local pidfile pid proc cmdline
  pidfile="$(pid_file)"
  if [[ -r "${pidfile}" ]]; then
    pid="$(tr -d '[:space:]' <"${pidfile}" 2>/dev/null || true)"
    if process_exists "${pid}" && is_mindfs_pid "${pid}"; then
      printf '%s\n' "${pid}"
      return 0
    fi
  fi

  for proc in /proc/[0-9]*; do
    pid="${proc##*/}"
    [[ -r "${proc}/cmdline" ]] || continue
    cmdline="$(tr '\0' ' ' <"${proc}/cmdline" 2>/dev/null || true)"
    if is_mindfs_pid "${pid}" && [[ "${cmdline}" == *"${ADDR}"* ]]; then
      printf '%s\n' "${pid}"
      return 0
    fi
  done
  return 1
}

container_id() {
  awk -F/ '/\/docker\// {print $NF; exit}' /proc/1/cgroup 2>/dev/null || true
}

health_check() {
  if bash -c "exec 3<>/dev/tcp/${ADDR%:*}/${ADDR##*:}; printf 'GET /health HTTP/1.0\r\n\r\n' >&3; read -r line <&3; [[ \"\$line\" == *'200 OK'* ]]" 2>/dev/null; then
    return 0
  fi
  return 1
}

restart_systemd() {
  systemctl daemon-reload
  systemctl restart "${SERVICE_NAME}"
  systemctl --no-pager --full status "${SERVICE_NAME}" --lines=20
}

compose_restart() {
  if [[ ! -f "${COMPOSE_FILE}" ]]; then
    return 1
  fi
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    docker compose -f "${COMPOSE_FILE}" down --remove-orphans || true
    docker rm -f "${COMPOSE_SERVICE}" >/dev/null 2>&1 || true
    docker compose -f "${COMPOSE_FILE}" up -d --build --force-recreate "${COMPOSE_SERVICE}"
    return 0
  fi
  if command -v docker-compose >/dev/null 2>&1; then
    docker-compose -f "${COMPOSE_FILE}" down --remove-orphans || true
    docker rm -f "${COMPOSE_SERVICE}" >/dev/null 2>&1 || true
    docker-compose -f "${COMPOSE_FILE}" up -d --build --force-recreate "${COMPOSE_SERVICE}"
    return 0
  fi
  return 1
}

print_restart_commands() {
  local cid="${1:-}"
  if [[ -f "${COMPOSE_FILE}" ]]; then
    echo "Preferred Docker Compose recreate command from the host:" >&2
    echo "  docker rm -f ${COMPOSE_SERVICE} 2>/dev/null || true" >&2
    echo "  docker compose -f ${COMPOSE_FILE} up -d --build --force-recreate ${COMPOSE_SERVICE}" >&2
    echo "or, with legacy docker-compose:" >&2
    echo "  docker rm -f ${COMPOSE_SERVICE} 2>/dev/null || true" >&2
    echo "  docker-compose -f ${COMPOSE_FILE} up -d --build --force-recreate ${COMPOSE_SERVICE}" >&2
    return
  fi
  if [[ -n "${cid}" ]]; then
    echo "Run this from the Docker host instead:" >&2
    echo "  docker restart ${cid}" >&2
  else
    echo "Restart this container from the host or supervisor." >&2
  fi
}

if [[ ! -x "${BIN}" ]]; then
  echo "mindfs binary is not executable: ${BIN}" >&2
  exit 1
fi

pid="$(find_mindfs_pid || true)"
if [[ "${pid}" == "1" ]]; then
  cid="$(container_id)"
  echo "mindfs is running as PID 1 in this Docker container." >&2
  echo "A script inside the container cannot safely restart PID 1 without stopping the container." >&2
  print_restart_commands "${cid}"
  exit 2
fi

if command -v systemctl >/dev/null 2>&1 &&
  [[ "$(systemctl show "${SERVICE_NAME}" -p LoadState --value 2>/dev/null || true)" == "loaded" ]]; then
  restart_systemd
  exit 0
fi

if compose_restart; then
  if health_check; then
    "${BIN}" -addr "${ADDR}" -status || true
  fi
  exit 0
fi

"${BIN}" -addr "${ADDR}" -restart
if health_check; then
  "${BIN}" -addr "${ADDR}" -status || true
  exit 0
fi

echo "mindfs restart command returned, but health check failed for ${ADDR}" >&2
exit 1

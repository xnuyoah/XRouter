#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_URL="${XROUTER_URL:-http://127.0.0.1:11016}"
MANAGEMENT_KEY="${XROUTER_MANAGEMENT_KEY:-}"
BACKUP_DIR="${XROUTER_BACKUP_DIR:-${ROOT_DIR}/backups}"
CONFIG_PATH="${XROUTER_CONFIG_PATH:-${ROOT_DIR}/config.yaml}"
AUTH_PATH="${XROUTER_AUTH_PATH:-${ROOT_DIR}/auths}"
WEBSEARCH_PATH="${XROUTER_WEBSEARCH_PATH:-${ROOT_DIR}/websearch}"
CHANNEL_STATUS_PATH="${XROUTER_CHANNEL_STATUS_PATH:-${ROOT_DIR}/channel-status}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
WORK_DIR="${BACKUP_DIR}/${STAMP}"
ARCHIVE="${BACKUP_DIR}/xrouter-${STAMP}.tar.gz"

if [[ -z "${MANAGEMENT_KEY}" ]]; then
  printf 'XROUTER_MANAGEMENT_KEY is required for a consistent analytics backup.\n' >&2
  printf 'Example: XROUTER_MANAGEMENT_KEY=... %s\n' "${BASH_SOURCE[0]}" >&2
  exit 1
fi

install -d -m 700 "${BACKUP_DIR}"
install -d -m 700 "${WORK_DIR}"

checksum_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

if [[ -f "${CONFIG_PATH}" ]]; then
  cp -p "${CONFIG_PATH}" "${WORK_DIR}/config.yaml"
fi

for state_path in "${AUTH_PATH}" "${WEBSEARCH_PATH}" "${CHANNEL_STATUS_PATH}"; do
  if [[ -d "${state_path}" ]]; then
    tar -czf "${WORK_DIR}/$(basename "${state_path}")-state.tar.gz" \
      -C "$(dirname "${state_path}")" "$(basename "${state_path}")"
  fi
done

CURL_CONFIG="$(mktemp "${BACKUP_DIR}/.curlrc.XXXXXX")"
chmod 600 "${CURL_CONFIG}"
trap 'rm -f "${CURL_CONFIG}"' EXIT
printf 'header = "Authorization: Bearer %s"\n' "${MANAGEMENT_KEY}" > "${CURL_CONFIG}"
curl -fsS --config "${CURL_CONFIG}" \
  "${BASE_URL}/v8/management/observability/usage/backup" \
  -o "${WORK_DIR}/usage-analytics.db"
if [[ ! -s "${WORK_DIR}/usage-analytics.db" ]]; then
  printf 'Usage analytics backup is empty.\n' >&2
  exit 1
fi

{
  printf 'created_at=%s\n' "${STAMP}"
  printf 'base_url=%s\n' "${BASE_URL}"
  printf 'git_commit=%s\n' "$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || printf 'unknown')"
  for file in "${WORK_DIR}"/*; do
    [[ -f "${file}" ]] || continue
    [[ "$(basename "${file}")" == "manifest.txt" ]] && continue
    sum_line="$(checksum_file "${file}")"
    printf '%s  %s\n' "${sum_line%% *}" "$(basename "${file}")"
  done
} > "${WORK_DIR}/manifest.txt"

tar -czf "${ARCHIVE}" -C "${BACKUP_DIR}" "${STAMP}"
rm -rf "${WORK_DIR}"
printf 'Backup created: %s\n' "${ARCHIVE}"

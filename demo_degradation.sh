#!/usr/bin/env bash
# Демонстрация практической №13: как приём телеметрии работает при сбоях.

set -euo pipefail

registry='http://127.0.0.1:5000'
telemetry='http://127.0.0.1:5001'
notifications='http://127.0.0.1:5002'
body=$(mktemp)
trap 'rm -f "$body"' EXIT

# Показываем только HTTP-код и поля, важные для этого шага.
request() {
  local label=$1 expected=$2 filter=$3 code summary
  shift 3
  code=$(curl -sS --max-time 5 -o "$body" -w '%{http_code}' "$@")
  if [[ "$code" != "$expected" ]] || ! summary=$(jq -ce "$filter" "$body"); then
    printf 'ОШИБКА: %s — HTTP %s, ожидался HTTP %s и заданные поля\n' "$label" "$code" "$expected" >&2
    cat "$body" >&2
    exit 1
  fi
  printf '%s → HTTP %s %s\n' "$label" "$code" "$summary"
}

compose() {
  local output
  printf '+ docker compose %s %s\n' "$1" "$2"
  if ! output=$(docker compose "$@" 2>&1); then
    printf '%s\n' "$output" >&2
    exit 1
  fi
}

wait_ready() {
  curl -fsS --max-time 3 --retry 8 --retry-connrefused --retry-delay 1 \
    "$1/healthz" >/dev/null
}

# Новый ID позволяет повторять сценарий на том же стенде.
source_id="building-$(date +%s)-$$"
source=$(jq -nc --arg id "$source_id" '{id:$id,name:"Учебный источник",enabled:true}')
alarm=$(jq -nc --arg id "$source_id" --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{source_id:$id,stream_id:"temp",seq:1,measured_at:$ts,kind:"alarm",value:95}')
json=(-H 'Content-Type: application/json')

printf '%s\n' 'Практическая №13: приём тревоги при сбоях'
request 'POST /sources — регистрация' 201 'select(.enabled == true) | {id,enabled}' \
  "${json[@]}" --data "$source" "$registry/sources"

printf '\n%s\n' '1. Сервис уведомлений отключён'
compose stop notification-service
printf '%s\n' 'Тревога сохраняется (201); уведомление ожидает доставки (pending).'
request 'POST /measurements — приём тревоги' 201 \
  'select(.notification_status == "pending") | {seq,notification_status}' \
  "${json[@]}" --data "$alarm" "$telemetry/measurements"
id=$(jq -er '.id | select(type == "string" and length > 0)' "$body")
request 'GET /measurements/{id} — запись доступна' 200 \
  'select(.notification_status == "pending") | {seq,notification_status}' "$telemetry/measurements/$id"

printf '\n%s\n' '2. Сервис уведомлений восстановлен'
compose start notification-service
wait_ready "$notifications"
printf '%s\n' 'Повторяем доставку для той же записи. Статус sent означает, что уведомление создано.'
request 'POST /measurements/{id}/notification — повтор доставки' 200 \
  'select(.notification_status == "sent") | {seq,notification_status}' \
  -X POST "$telemetry/measurements/$id/notification"
request 'GET /notifications/{id} — уведомление найдено' 200 \
  '{text,read}' "$notifications/notifications/$id"

printf '\n%s\n' '3. Реестр источников отключён'
compose stop source-service
printf '%s\n' \
  'Новый пакет: проверить источник нельзя, поэтому пакет не сохраняется (503).' \
  'Прежняя запись: телеметрия читает её из своего хранилища без реестра (200).'
next_alarm=$(jq '.seq = 2' <<<"$alarm")
request 'POST /measurements — новый пакет (seq=2)' 503 \
  'select(.error == "source_unavailable") | {error}' \
  "${json[@]}" --data "$next_alarm" "$telemetry/measurements"
request 'GET /measurements/{id} — прежняя запись (seq=1)' 200 \
  'select(.seq == 1 and .notification_status == "sent") | {seq,notification_status}' \
  "$telemetry/measurements/$id"

printf '\n'
compose start source-service
wait_ready "$registry"
printf '%s\n' 'Все проверки пройдены. Стенд оставлен запущенным.'

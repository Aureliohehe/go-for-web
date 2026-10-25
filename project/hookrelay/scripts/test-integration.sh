#!/usr/bin/env bash
# Creates ONLY disposable containers/databases. Does not source .env or use Compose.
set -euo pipefail
cd "$(dirname "$0")/.."
logs=$(mktemp -d /tmp/hookrelay-integration-XXXXXX)
name="hookrelay-test-$(date +%s)-$$"
mysql_id=""
redis_id=""
cleanup() {
  # IDs belong exclusively to this invocation; never stop existing user services.
  if [[ -n "$mysql_id" ]]; then docker rm -fv "$mysql_id" >/dev/null; fi
  if [[ -n "$redis_id" ]]; then docker rm -fv "$redis_id" >/dev/null; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
printf 'Test logs: %s\n' "$logs"
export MYSQL_ROOT_PASSWORD="$(openssl rand -hex 24)"
mysql_id=$(docker run -d --name "$name-mysql" -p 127.0.0.1::3306 \
  -e MYSQL_ROOT_PASSWORD -e MYSQL_ROOT_HOST=% -e MYSQL_DATABASE=hookrelay_test mysql:8.4)
unset MYSQL_ROOT_PASSWORD
redis_id=$(docker run -d --name "$name-redis" -p 127.0.0.1::6379 redis:7.4-alpine)
mysql_exec() {
  docker exec -i "$mysql_id" sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot "$@"' sh "$@"
}
ready=0
for ((i=0; i<90; i++)); do
  if mysql_exec -e 'SELECT 1' >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [[ "$ready" != 1 ]]; then
  docker logs "$mysql_id" >"$logs/mysql-startup.log" 2>&1
  echo 'MySQL startup failed; see test logs' >&2
  exit 1
fi
# Container-local exec can see the initialization server before its final TCP
# listener starts. Wait for the network listener too, using the container's client.
for ((i=0; i<30; i++)); do
  if mysql_exec --protocol=TCP -h127.0.0.1 -e 'SELECT 1' >/dev/null 2>&1; then break; fi
  sleep 1
done
mysql_exec -e 'CREATE DATABASE receiver_test;'
for migration in migrations/*.up.sql; do mysql_exec hookrelay_test < "$migration"; done
mysql_exec receiver_test < migrations/receiver/000001_create_receipts.up.sql
export MYSQL_HOST=127.0.0.1
export MYSQL_PORT="$(docker port "$mysql_id" 3306/tcp | cut -d: -f2)"
export MYSQL_DATABASE=hookrelay_test MYSQL_USER=root
# Read only the password generated for THIS disposable container, never user .env.
export MYSQL_PASSWORD="$(docker exec "$mysql_id" printenv MYSQL_ROOT_PASSWORD)"
export REDIS_ADDR="$(docker port "$redis_id" 6379/tcp)"
export RECEIVER_MYSQL_HOST=127.0.0.1 RECEIVER_MYSQL_PORT="$MYSQL_PORT"
export RECEIVER_MYSQL_DATABASE=receiver_test RECEIVER_MYSQL_USER=root RECEIVER_MYSQL_PASSWORD="$MYSQL_PASSWORD"
export API_KEY=integration-only ALLOWED_ENDPOINT_URL=http://127.0.0.1:9090/webhook
export MAX_PENDING_DELIVERIES=10000 RETRY_BASE_DELAY=5s RETRY_MAX_DELAY=1m
export HTTP_ADDR=127.0.0.1:8080
unset HOOKRELAY_INTEGRATION HOOKRELAY_ADMISSION_INTEGRATION HOOKRELAY_OUTBOX_INTEGRATION HOOKRELAY_RECEIVER_INTEGRATION
HOOKRELAY_INTEGRATION=1 go test -race -count=1 -v ./internal/store ./cmd/api 2>&1 | tee "$logs/store.log"
HOOKRELAY_ADMISSION_INTEGRATION=1 go test -race -count=1 -run TestAdmissionIntegration -v ./internal/store 2>&1 | tee "$logs/admission.log"
HOOKRELAY_RECEIVER_INTEGRATION=1 go test -race -count=3 -v ./internal/receiver 2>&1 | tee "$logs/receiver.log"
HOOKRELAY_OUTBOX_INTEGRATION=1 HOOKRELAY_RECEIVER_INTEGRATION=1 go test -race -count=1 -v ./tests/integration 2>&1 | tee "$logs/queue.log"

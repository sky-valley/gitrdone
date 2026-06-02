#!/usr/bin/env bash
set -euo pipefail

container_name="${GITRDONE_POSTGRES_TEST_CONTAINER:-gitrdone-postgres-test}"
image="${GITRDONE_POSTGRES_TEST_IMAGE:-postgres:17}"
port="${GITRDONE_POSTGRES_TEST_PORT:-55432}"
password="${GITRDONE_POSTGRES_TEST_PASSWORD:-postgres}"

cleanup() {
  docker rm -f "$container_name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cleanup
docker run --rm -d \
  --name "$container_name" \
  -e POSTGRES_PASSWORD="$password" \
  -p "127.0.0.1:${port}:5432" \
  "$image" >/dev/null

for _ in $(seq 1 60); do
  if docker exec "$container_name" pg_isready -U postgres >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

docker exec "$container_name" pg_isready -U postgres >/dev/null

database_url="postgres://postgres:${password}@127.0.0.1:${port}/postgres?sslmode=disable"
GITRDONE_TEST_DATABASE_URL="$database_url" go test ./internal/http -run TestPostgres -count=1

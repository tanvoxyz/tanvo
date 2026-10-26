#!/usr/bin/env bash
# Runs the matrix of docs/benchmarks.md §1 on fresh containers and prints
# one line per run. Needs Docker; ports 55432 and 56379 must be free.
set -euo pipefail
cd "$(dirname "$0")"

docker rm -f qb-pg qb-valkey >/dev/null 2>&1 || true
docker run -d --name qb-pg -p 55432:5432 -e POSTGRES_USER=b -e POSTGRES_PASSWORD=b -e POSTGRES_DB=b \
  postgres:18 -c max_connections=300 -c shared_buffers=1GB -c idle_in_transaction_session_timeout=10s >/dev/null
docker run -d --name qb-valkey -p 56379:6379 valkey/valkey:8.1.10 --appendonly yes --appendfsync everysec >/dev/null
trap 'docker rm -f qb-pg qb-valkey >/dev/null' EXIT
until docker exec qb-pg pg_isready -U b -d b >/dev/null 2>&1; do sleep 1; done
sleep 2

CGO_ENABLED=0 go build -o qb .
r() { ./qb "$@"; }
for w in noop pg; do
  r -system river -work $w -n 30000
  for cd in 10ms 25ms 50ms; do r -system river -work $w -n 50000 -cooldown $cd; done
  r -system river -work $w -n 50000 -cooldown 25ms -workers 200
  r -system stream -work $w -n 50000
  r -system outbox -work $w -n 50000
  r -system stream -work $w -n 50000 -producers 96
  r -system river -work $w -n 50000 -producers 96 -cooldown 25ms -workers 200
  r -system outbox -work $w -n 50000 -producers 96
done
for rt in 500 2000; do
  r -system river -mode rate -rate $rt -dur 20s -work pg
  r -system river -mode rate -rate $rt -dur 20s -work pg -cooldown 25ms
  r -system stream -mode rate -rate $rt -dur 20s -work pg
  r -system outbox -mode rate -rate $rt -dur 20s -work pg
done

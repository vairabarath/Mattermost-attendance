#!/usr/bin/env bash
# Runs all tests, including the Postgres integration tests, using only
# Docker: a throwaway Postgres (docker-compose.test.yml) and the Go toolchain
# in a container. Nothing touches the real attendance database.
#
#   ./scripts/test-integration.sh            # go test ./...
#   ./scripts/test-integration.sh -run Fix   # extra args go to go test
set -euo pipefail
cd "$(dirname "$0")/.."

GO_IMAGE="${GO_IMAGE:-golang:1.27-alpine}"
compose() { docker compose -f docker-compose.test.yml "$@"; }

cleanup() { compose down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

compose up -d --wait test-db
network="attendance-test_default"

docker run --rm \
  --network "$network" \
  -v "$PWD":/src:ro -w /src \
  -v attendance-test-gocache:/root/.cache/go-build \
  -v attendance-test-gomod:/go/pkg/mod \
  -e GOFLAGS=-buildvcs=false \
  -e TEST_DATABASE_URL="postgres://test:test@test-db:5432/test?sslmode=disable" \
  "$GO_IMAGE" \
  go test -count=1 "$@" ./...

#!/usr/bin/env bash
set -euo pipefail
echo "Starting worker..."
docker compose up -d worker
echo "worker deployed"

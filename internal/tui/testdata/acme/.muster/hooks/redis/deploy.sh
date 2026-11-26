#!/usr/bin/env bash
set -euo pipefail
echo "Starting redis..."
docker compose up -d redis
echo "redis deployed"

#!/usr/bin/env bash
set -euo pipefail
echo "Starting api..."
docker compose up -d api
echo "api deployed"

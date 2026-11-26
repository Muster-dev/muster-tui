#!/usr/bin/env bash
set -euo pipefail
echo "Starting web..."
docker compose up -d web
echo "web deployed"

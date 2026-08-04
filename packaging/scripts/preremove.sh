#!/bin/sh
set -eu

SERVICE_NAME="ollama-gateway.service"

if command -v systemctl >/dev/null 2>&1; then
  systemctl stop "$SERVICE_NAME" || true
fi

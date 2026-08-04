#!/bin/sh
set -eu

SERVICE_NAME="ollama-gateway.service"

if command -v systemctl >/dev/null 2>&1; then
  case "${1:-}" in
    0|remove|purge)
      systemctl disable "$SERVICE_NAME" || true
      ;;
    *)
      ;;
  esac
  systemctl daemon-reload || true
fi

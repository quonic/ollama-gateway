#!/bin/sh
set -eu

SERVICE_NAME="ollama-gateway.service"
SERVICE_USER="ollama-gateway"
SERVICE_GROUP="ollama-gateway"

if [ -d /var/lib/ollama-gateway ]; then
  chown "$SERVICE_USER:$SERVICE_GROUP" /var/lib/ollama-gateway
  chmod 0750 /var/lib/ollama-gateway
fi
if [ -d /var/log/ollama-gateway ]; then
  chown "$SERVICE_USER:$SERVICE_GROUP" /var/log/ollama-gateway
  chmod 0750 /var/log/ollama-gateway
fi
if [ -f /etc/ollama-gateway/config.yaml ]; then
  chown root:"$SERVICE_GROUP" /etc/ollama-gateway/config.yaml
  chmod 0640 /etc/ollama-gateway/config.yaml
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true

  case "${1:-}" in
    1|configure)
      if [ "${2:-}" = "" ]; then
        systemctl enable --now "$SERVICE_NAME" || true
      else
        systemctl try-restart "$SERVICE_NAME" || true
      fi
      ;;
    2)
      systemctl try-restart "$SERVICE_NAME" || true
      ;;
    *)
      ;;
  esac
fi

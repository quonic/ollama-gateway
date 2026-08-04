#!/bin/sh
set -eu

SERVICE_USER="ollama-gateway"
SERVICE_GROUP="ollama-gateway"
STATE_DIR="/var/lib/ollama-gateway"
LOG_DIR="/var/log/ollama-gateway"
RUNTIME_DIR="/var/run/ollama-gateway"

if ! getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
  groupadd --system "$SERVICE_GROUP"
fi

if ! getent passwd "$SERVICE_USER" >/dev/null 2>&1; then
  NOLOGIN="/usr/sbin/nologin"
  if [ ! -x "$NOLOGIN" ]; then
    NOLOGIN="/sbin/nologin"
  fi
  if [ ! -x "$NOLOGIN" ]; then
    NOLOGIN="/bin/false"
  fi

  useradd \
    --system \
    --gid "$SERVICE_GROUP" \
    --home-dir "$STATE_DIR" \
    --shell "$NOLOGIN" \
    --comment "Ollama Gateway service account" \
    "$SERVICE_USER"
fi

install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_GROUP" "$STATE_DIR"
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_GROUP" "$LOG_DIR"
install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_GROUP" "$RUNTIME_DIR"
install -d -m 0750 -o root -g "$SERVICE_GROUP" /etc/ollama-gateway

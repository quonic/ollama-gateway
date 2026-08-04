#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if ! command -v nfpm >/dev/null 2>&1; then
  echo "nfpm not found. Install with: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest" >&2
  exit 1
fi

VERSION="${VERSION:-$(git describe --tags --always --dirty | sed 's/^v//')}"

ARCH="${ARCH:-}"
if [[ -z "$ARCH" ]]; then
  case "$(uname -m)" in
    x86_64|amd64)
      ARCH="amd64"
      ;;
    aarch64|arm64)
      ARCH="arm64"
      ;;
    *)
      echo "unsupported architecture: $(uname -m). Set ARCH explicitly." >&2
      exit 1
      ;;
  esac
fi

mkdir -p bin/packages

go build -o bin/ollama-gateway ./cmd/gateway

for packager in deb rpm; do
  target="bin/packages/ollama-gateway_${VERSION}_linux_${ARCH}.${packager}"
  VERSION="$VERSION" ARCH="$ARCH" nfpm package \
    --config packaging/nfpm.yaml \
    --packager "$packager" \
    --target "$target"
  echo "built $target"
done

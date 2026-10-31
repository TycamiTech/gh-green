#!/usr/bin/env bash
# ==============================================================================
# gh-green Build Script
# Compiles a minimal, statically-linked production binary for Linux amd64.
# ==============================================================================

set -euo pipefail

OUTPUT_BINARY="gh-green"

echo "[BUILD] Compiling gh-green production binary for Linux (amd64)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "${OUTPUT_BINARY}" main.go

echo "[BUILD] Successfully built ${OUTPUT_BINARY} ($(du -h "${OUTPUT_BINARY}" 2>/dev/null | cut -f1 || echo "ready"))"


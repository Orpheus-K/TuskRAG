#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${1:-dist/sbom}"
mkdir -p "$OUT_DIR"

echo "Generating lockfile-backed dependency inventory and SHA-256 checksums in $OUT_DIR..."

cp -f services/core/go.sum "$OUT_DIR/go.sum"
cp -f services/parser/uv.lock "$OUT_DIR/uv.lock"
cp -f pnpm-lock.yaml "$OUT_DIR/pnpm-lock.yaml"

(
  cd "$OUT_DIR"
  sha256sum go.sum uv.lock pnpm-lock.yaml > SHA256SUMS
)

if command -v syft >/dev/null 2>&1; then
  syft dir:. -o cyclonedx-json="$OUT_DIR/tuskrag.cdx.json"
  (
    cd "$OUT_DIR"
    sha256sum tuskrag.cdx.json >> SHA256SUMS
  )
else
  echo "syft not installed; lockfile snapshots and SHA256SUMS generated."
fi

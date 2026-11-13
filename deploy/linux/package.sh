#!/bin/bash
# Package previously built artifacts. No online dependency resolution.
set -euo pipefail
umask 077
[[ $# == 3 ]] || { echo 'usage: package.sh CORE_BINARY PARSER_RUNTIME NEW_OUTPUT_DIRECTORY' >&2; exit 2; }
core=$(realpath -e "$1")
parser=$(realpath -e "$2")
output=$(realpath -m "$3")
repo=$(cd -- "$(dirname -- "$0")/../.." && pwd)
[[ -x "$core" && -x "$parser/tuskrag-parse" && ! -e "$output" ]]
mkdir -m 0700 "$output"
stage="$output/tuskrag"
mkdir -p "$stage/deploy/linux" "$stage/parser"
install -m 0755 "$core" "$stage/tuskrag"
# The parser runtime contract contains only these three paths; credentials and
# unrelated build files are never copied from its parent directory.
cp -a "$parser/python" "$parser/src" "$parser/tuskrag-parse" "$stage/parser/"
cp "$repo/deploy/linux/"*.sh "$repo/deploy/linux/tuskrag.service" "$repo/deploy/linux/tuskrag.env.example" "$repo/deploy/linux/README.md" "$stage/deploy/linux/"
cp -a "$repo/deploy/licenses" "$stage/deploy/"
cp "$repo/LICENSE" "$stage/"
chmod 0755 "$stage/deploy/linux/"*.sh
"$stage/tuskrag" openapi > "$stage/openapi.json"
"$stage/parser/tuskrag-parse" version > "$stage/parser-version.json"
# Artifacts are installed as root but executed by the unprivileged service.
# Keep the output directory private; normalize only the credential-free payload.
find "$stage" -type d -exec chmod 0755 {} +
find "$stage" -type f -perm /111 -exec chmod 0755 {} +
find "$stage" -type f ! -perm /111 -exec chmod 0644 {} +
(
  cd "$stage"
  find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
)
chmod 0644 "$stage/SHA256SUMS"
tar --create --gzip --file "$output/tuskrag-linux-amd64.tar.gz" --directory "$output" tuskrag
(cd "$output" && sha256sum tuskrag-linux-amd64.tar.gz > tuskrag-linux-amd64.tar.gz.sha256)
printf 'Packaged: %s/tuskrag-linux-amd64.tar.gz\n' "$output"

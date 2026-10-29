#!/bin/sh
# Experimental S6 probe only; this configuration is not a deployment profile.
set -eu
: "${TUSKRAG_S6_DOCKER:?set an absolute Docker CLI path}"
: "${TUSKRAG_S6_DOCKER_HOST:?set the isolated Docker socket}"
: "${TUSKRAG_S6_IMAGE:?set an existing local image with sh and runuser}"
: "${TUSKRAG_S6_TEST_BINARY:?set the absolute parser test binary path}"
: "${TUSKRAG_S6_BWRAP:?set the absolute bubblewrap binary path}"
: "${TUSKRAG_S6_RUNTIME:?set the absolute self-contained Python parser runtime directory}"
: "${TUSKRAG_S6_SECCOMP:?set the absolute experimental seccomp profile path}"
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
temp=$(mktemp -d "${TMPDIR:-/tmp}/tuskrag-s6.XXXXXXXX")
parent=
cleanup() {
  rm -f "$temp/python3"
  rmdir "$temp"
  if [ -n "$parent" ]; then
    if [ -d "$parent/manager" ]; then rmdir "$parent/manager"; fi
    rmdir "$parent"
  fi
}
trap cleanup EXIT
parent=$(mktemp -d /sys/fs/cgroup/tuskrag-s6.XXXXXXXX)
chmod 755 "$parent"
printf '+cpu +memory +pids' > "$parent/cgroup.subtree_control"
mkdir "$parent/manager"
chown 65534:65534 "$parent" "$parent/cgroup.procs" "$parent/cgroup.subtree_control" "$parent/manager"
cat > "$temp/python3" <<'EOF'
#!/bin/sh
exec /usr/tuskrag-python/bin/python3.13 "$@"
EOF
chmod 755 "$temp/python3"
"$TUSKRAG_S6_DOCKER" --host "$TUSKRAG_S6_DOCKER_HOST" run --rm --network none --read-only --cgroupns=host \
  --security-opt "seccomp=$TUSKRAG_S6_SECCOMP" --security-opt systempaths=unconfined \
  --cap-drop ALL --cap-add SETUID --cap-add SETGID \
  --tmpfs /tmp:rw,nosuid,nodev,size=256m --tmpfs /tmp-tests:rw,exec,nosuid,nodev,size=256m,mode=1777 \
  --mount type=bind,src="$parent",dst=/cg \
  --mount type=bind,src="$TUSKRAG_S6_TEST_BINARY",dst=/test,readonly \
  --mount type=bind,src="$TUSKRAG_S6_BWRAP",dst=/bwrap,readonly \
  --mount type=bind,src="$TUSKRAG_S6_RUNTIME",dst=/runtime,readonly \
  --mount type=bind,src="$TUSKRAG_S6_RUNTIME/python",dst=/usr/tuskrag-python,readonly \
  --mount type=bind,src="$temp/python3",dst=/usr/bin/python3,readonly \
  --mount type=bind,src="$root/services/core/internal/parser",dst=/src/services/core/internal/parser,readonly \
  --mount type=bind,src="$root/services/parser/tests/fixtures/csv",dst=/src/services/parser/tests/fixtures/csv,readonly \
  --mount type=bind,src="$root/services/parser/tests/fixtures/docx",dst=/src/services/parser/tests/fixtures/docx,readonly \
  --mount type=bind,src="$root/services/parser/tests/fixtures/xlsx",dst=/src/services/parser/tests/fixtures/xlsx,readonly \
  -e TUSKRAG_TEST_CGROUP=/cg -e TUSKRAG_TEST_BWRAP=/bwrap -e TMPDIR=/tmp-tests \
  -e TUSKRAG_TEST_PARSER_RUNTIME=/runtime -e TUSKRAG_TEST_PARSER=/runtime/tuskrag-parse \
  -w /src/services/core/internal/parser --entrypoint /bin/sh "$TUSKRAG_S6_IMAGE" -c \
  'set -e; printf "%s" "$$" > /cg/manager/cgroup.procs; exec runuser -u nobody --preserve-environment -- /bin/sh -c '\''id; grep CapEff /proc/self/status; exec /test -test.run="Test(Sandbox|Parser|Python)" -test.v'\'''

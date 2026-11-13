#!/usr/bin/env bash
# Run compiled race binaries beside the isolated PostgreSQL instance.
set -euo pipefail

mode=${1:-shutdown}
case "$mode" in shutdown|native|process) ;; *) echo 'usage: test-process-linux.sh shutdown|native|process' >&2; exit 2 ;; esac
[[ $(uname -s) == Linux ]] || { echo 'Linux is required' >&2; exit 2; }
core=$(realpath "${TUSKRAG_TEST_PROCESS_BINARY:?compiled cmd/tuskrag race binary required}")
logs=$(realpath -m "${TUSKRAG_TEST_LOG_DIR:-./test-results/linux-process}")
[[ -x "$core" ]] || { echo 'core test binary is not executable' >&2; exit 2; }
mkdir -p "$logs"

run_test() {
  local binary=$1 pattern=$2 log=$3
  "$binary" -test.v -test.run "$pattern" -test.timeout 6m 2>&1 | tee "$logs/$log"
}

# A stale binary must not silently pass with "no tests to run".
require_test() {
  "$1" -test.list "^$2$" | grep -Fxq "$2" || { echo "missing compiled test: $2" >&2; exit 2; }
}

if [[ "$mode" == shutdown ]]; then
  : "${TUSKRAG_TEST_ADMIN_URL:?isolated database admin URL required}"
  : "${TUSKRAG_TEST_APP_URL:?isolated database app URL required}"
  jobs=$(realpath "${TUSKRAG_TEST_JOBS_BINARY:?compiled internal/jobs race binary required}")
  require_test "$jobs" TestRollbackClosedConnectionAfterCancellation
  require_test "$core" TestWrappedWorkerCancellation
  require_test "$core" TestServeWrappedCancellation
  require_test "$core" TestServePreservesJoinedCleanupFailure
  run_test "$jobs" '^Test' jobs-race.log
  run_test "$core" '^Test(Serve.*|WrappedWorkerCancellation|RunWorkerPreservesFailures)$' shutdown-main.log
  exit
fi

workspace=$(realpath "${TUSKRAG_TEST_WORKSPACE:?workspace or staged fixture tree required}")
: "${TUSKRAG_TEST_BWRAP:?bubblewrap executable required}"
: "${TUSKRAG_TEST_PARSER_RUNTIME:?parser runtime directory required}"
: "${TUSKRAG_TEST_PARSER:?parser executable required}"
help=$("$TUSKRAG_TEST_BWRAP" --help)
for flag in --disable-userns --ro-bind-fd --bind-fd; do
  grep -Fq -- "$flag" <<< "$help" || { echo "bubblewrap lacks $flag" >&2; exit 2; }
done
"$TUSKRAG_TEST_PARSER" version
[[ -r "$workspace/services/core/internal/parser/testdata/native-text/policy.md" ]]
require_test "$core" TestNativeTextSandboxMatchesBaseline
if [[ "$mode" == process ]]; then
  : "${TUSKRAG_TEST_ADMIN_URL:?isolated database admin URL required}"
  : "${TUSKRAG_TEST_APP_URL:?isolated database app URL required}"
  : "${TUSKRAG_TEST_MIGRATOR_URL:?isolated database migrator URL required}"
  [[ -r "$workspace/services/parser/tests/fixtures/csv/policy.csv" ]]
  require_test "$core" TestWorkerProcess
fi

# A caller may supply a delegated cgroup. Remove only a group created here.
if [[ -z "${TUSKRAG_TEST_CGROUP:-}" ]]; then
  export TUSKRAG_TEST_CGROUP="/sys/fs/cgroup/tuskrag-process-$$"
  mkdir "$TUSKRAG_TEST_CGROUP"
  trap 'rmdir "$TUSKRAG_TEST_CGROUP"' EXIT
  printf '+cpu +memory +pids' > "$TUSKRAG_TEST_CGROUP/cgroup.subtree_control"
fi
cd "$workspace/services/core/cmd/tuskrag"
run_test "$core" '^TestNativeTextSandboxMatchesBaseline$' native-race.log
if [[ "$mode" == process ]]; then
  run_test "$core" '^TestWorkerProcess$' process-race.log
fi

#!/bin/bash
set -euo pipefail
trap 'printf "TuskRAG startup failed at line %s\n" "$LINENO" >&2' ERR
installation=$(cd -- "$(dirname -- "$0")/../.." && pwd)
case "${TUSKRAG_ROLE:-}" in
  all|worker)
    relative=$(awk -F: '$1 == "0" {print $3}' /proc/self/cgroup)
    [[ "$relative" == /* && "$relative" != / ]]
    root="/sys/fs/cgroup$relative"
    [[ -f "$root/cgroup.controllers" && -w "$root/cgroup.subtree_control" ]]
    mkdir -p "$root/manager"
    # A cgroup with processes cannot enable domain controllers for children.
    printf '%s\n' "$$" > "$root/manager/cgroup.procs"
    printf '+cpu +memory +pids\n' > "$root/cgroup.subtree_control"
    export TUSKRAG_CGROUP_ROOT="$root"
    ;;
  api) ;;
  *) echo 'TUSKRAG_ROLE must be api, worker or all' >&2; exit 1 ;;
esac
exec "$installation/tuskrag"

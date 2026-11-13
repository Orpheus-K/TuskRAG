#!/bin/bash
# Restore only into a newly created empty database and a new storage directory.
set -euo pipefail
trap 'printf "Restore failed at line %s; keep the target stopped\n" "$LINENO" >&2' ERR
umask 077
[[ $# == 2 ]] || { echo 'usage: restore-local.sh BACKUP_DIRECTORY NEW_STORAGE_DIRECTORY' >&2; exit 2; }
: "${TUSKRAG_SERVICE_UNIT:?stopped target systemd unit required}"
: "${TUSKRAG_BINARY:?absolute core binary required}"
: "${TUSKRAG_DATABASE_URL:?target application database URL required for verification}"
: "${PGDATABASE:?new target database name required; use PGHOST/PGPORT/PGUSER/PGPASSFILE}"
[[ $(systemctl show --property=ActiveState --value "$TUSKRAG_SERVICE_UNIT") == inactive ]] || { echo 'stop the target service before restore' >&2; exit 1; }
[[ $PGDATABASE =~ ^[a-zA-Z_][a-zA-Z0-9_]{0,62}$ ]]
identity_sql="SELECT current_database()||'|'||COALESCE(inet_server_addr()::text,'local')||'|'||COALESCE(inet_server_port()::text,'local')"
management_identity=$(psql -XAt -v ON_ERROR_STOP=1 -c "$identity_sql")
application_identity=$(psql -XqAt -v ON_ERROR_STOP=1 -c '\getenv app_connection TUSKRAG_DATABASE_URL' -c '\connect -reuse-previous=off :app_connection' -c "$identity_sql")
[[ "$management_identity" == "$application_identity" ]] || { echo 'application and restore connections differ' >&2; exit 1; }
backup=$(realpath -e "$1")
destination=$(realpath -m "$2")
[[ ! -e "$destination" && "$destination" != "$backup/"* ]]
[[ $(cat "$backup/COMPLETE") == tuskrag-cold-local-v1 ]] || { echo 'incomplete or unknown backup' >&2; exit 1; }
(cd "$backup" && sha256sum --check --strict SHA256SUMS)
[[ $(psql -XAt -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND c.relkind IN ('r','p','m','S','v')") == 0 ]] || { echo 'restore database must be empty' >&2; exit 1; }
[[ $(psql -XAt -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend'") == 0 ]] || { echo 'disconnect every other target database client' >&2; exit 1; }
mkdir -m 0700 "$destination"
tar --extract --gzip --no-same-owner --no-same-permissions --file "$backup/originals.tar.gz" --directory "$destination"
pg_restore --exit-on-error --single-transaction --no-password --dbname "$PGDATABASE" < "$backup/database.dump"
export TUSKRAG_STORAGE_DIR="$destination"
tenants=$(psql -XAt -v ON_ERROR_STOP=1 -c 'SELECT id FROM tenant ORDER BY id')
while IFS= read -r tenant; do
  [[ -z "$tenant" ]] || "$TUSKRAG_BINARY" ops verify-storage "$tenant"
done <<< "$tenants"
printf 'Restore verified; keep service stopped until ownership, master key and retrieval checks pass: %s\n' "$destination"

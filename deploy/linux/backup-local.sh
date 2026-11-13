#!/bin/bash
# Cold backup for the single-host local-storage profile. No service is stopped
# automatically; the operator owns the maintenance window and all writers.
set -euo pipefail
trap 'printf "Backup failed at line %s; no complete backup was produced\n" "$LINENO" >&2' ERR
umask 077
[[ $# == 1 ]] || { echo 'usage: backup-local.sh NEW_BACKUP_DIRECTORY' >&2; exit 2; }
: "${TUSKRAG_SERVICE_UNIT:?single all-mode systemd unit required}"
: "${TUSKRAG_BINARY:?absolute core binary required}"
: "${TUSKRAG_DATABASE_URL:?application database URL required for verification}"
: "${TUSKRAG_STORAGE_DIR:?original storage directory required}"
: "${PGDATABASE:?source database name required; use PGHOST/PGPORT/PGUSER/PGPASSFILE}"
[[ $(systemctl show --property=ActiveState --value "$TUSKRAG_SERVICE_UNIT") == inactive ]] || { echo 'stop the service before backup' >&2; exit 1; }
[[ $PGDATABASE =~ ^[a-zA-Z_][a-zA-Z0-9_]{0,62}$ ]]
identity_sql="SELECT current_database()||'|'||COALESCE(inet_server_addr()::text,'local')||'|'||COALESCE(inet_server_port()::text,'local')"
management_identity=$(psql -XAt -v ON_ERROR_STOP=1 -c "$identity_sql")
# PGDATABASE treats a URL as a literal database name. psql reads the application
# URL from its environment so a password never becomes a command-line argument.
application_identity=$(psql -XqAt -v ON_ERROR_STOP=1 -c '\getenv app_connection TUSKRAG_DATABASE_URL' -c '\connect -reuse-previous=off :app_connection' -c "$identity_sql")
[[ "$management_identity" == "$application_identity" ]] || { echo 'application and backup connections differ' >&2; exit 1; }
[[ $(psql -XAt -v ON_ERROR_STOP=1 -c "SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user") == t ]] || { echo 'backup role must read all tenant rows' >&2; exit 1; }
[[ $(psql -XAt -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend'") == 0 ]] || { echo 'disconnect every other database client before backup' >&2; exit 1; }
source=$(realpath -e "$TUSKRAG_STORAGE_DIR")
destination=$(realpath -m "$1")
[[ -d "$source" && "$destination" != "$source" && "$destination" != "$source/"* && ! -e "$destination" ]]
mkdir -m 0700 "$destination"
# Missing COMPLETE means an interrupted or failed backup; never restore it.
tenants=$(psql -XAt -v ON_ERROR_STOP=1 -c 'SELECT id FROM tenant ORDER BY id')
while IFS= read -r tenant; do
  [[ -z "$tenant" ]] || "$TUSKRAG_BINARY" ops verify-storage "$tenant" >> "$destination/storage-verification.jsonl"
done <<< "$tenants"
touch "$destination/storage-verification.jsonl"
psql -XAt -v ON_ERROR_STOP=1 -c "SELECT 'schema='||version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1; SELECT extname||'='||extversion FROM pg_extension ORDER BY extname; SELECT 'server='||current_setting('server_version')" > "$destination/database-version.txt"
pg_dump --format=custom --no-password > "$destination/database.dump"
tar --create --gzip --file "$destination/originals.tar.gz" --directory "$source" .
(
  cd "$destination"
  sha256sum database.dump originals.tar.gz database-version.txt storage-verification.jsonl > SHA256SUMS
)
printf 'tuskrag-cold-local-v1\n' > "$destination/COMPLETE"
printf 'Backup completed: %s\n' "$destination"

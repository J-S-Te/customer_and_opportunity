#!/usr/bin/env bash
# Incident-specific operation. Run on the deployment host; default is read-only.
set -euo pipefail
mode=${1:---inspect}
case "$mode" in --inspect|--apply) ;; *) echo 'Usage: bash script [--inspect|--apply]' >&2; exit 2;; esac
container=basic-platform-production-customer-mysql-1
predicate="application_code='customer_and_opportunity' AND environment_code='dev' AND last_error_code='PLATFORM_AUDIT_OUTBOX_SOURCE_MISMATCH' AND delivery_status='RETRY' AND occurred_at<'2026-08-12 00:00:00' AND next_attempt_at IS NOT NULL AND (locked_until IS NULL OR locked_until<UTC_TIMESTAMP(3))"
sql() {
  docker exec -i "$container" sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot --batch --skip-column-names customer_opportunity'
}
count=$(sql <<< "SELECT COUNT(*) FROM application_request_audit_outbox WHERE $predicate;")
echo "Eligible historical CRM events: $count"
[[ "$mode" == --apply ]] || exit 0
[[ "$count" =~ ^[0-9]+$ ]] && (( count <= 23200 )) || { echo 'Unexpected scope; aborting' >&2; exit 1; }
(( count > 0 )) || exit 0
umask 077
archive=$(mktemp -d /opt/basic-platform/backups/crm-audit-hold-20260907-XXXXXXXX)
exec 9>/opt/basic-platform/backups/.crm-audit-hold.lock
flock -n 9 || { echo 'Another hold operation is running' >&2; exit 1; }
docker exec "$container" sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -uroot --single-transaction --quick --no-tablespaces --set-gtid-purged=OFF customer_opportunity application_request_audit_outbox' | gzip > "$archive/request-audit-before.sql.gz"
gzip -t "$archive/request-audit-before.sql.gz"
sha256sum "$archive/request-audit-before.sql.gz" > "$archive/SHA256SUMS"
printf 'operation=hold-source-mismatch\ntime=%s\neligible=%s\npredicate=%s\n' "$(date -Is)" "$count" "$predicate" > "$archive/operation.txt"
total=0
for ((batch=0; batch<47; batch++)); do
  changed=$(sql <<< "SET SESSION innodb_lock_wait_timeout=5; UPDATE application_request_audit_outbox SET next_attempt_at=NULL,locked_by='',locked_until=NULL,updated_at=UTC_TIMESTAMP(3) WHERE $predicate ORDER BY id LIMIT 500; SELECT ROW_COUNT();")
  [[ "$changed" =~ ^[0-9]+$ ]] || { echo 'Invalid batch result' >&2; exit 1; }
  total=$((total + changed))
  printf 'batch=%s rows=%s\n' "$batch" "$changed" >> "$archive/operation.txt"
  (( changed > 0 )) || break
done
remaining=$(sql <<< "SELECT COUNT(*) FROM application_request_audit_outbox WHERE $predicate;")
printf 'held=%s remaining=%s\n' "$total" "$remaining" | tee -a "$archive/operation.txt"
echo "Verified backup and operation record: $archive"
[[ "$remaining" == 0 ]]

#!/usr/bin/env bash
set -Eeuo pipefail

# 导入客户与商机管理测试数据到本地 docker 的 MySQL。
# 用法：bash scripts/testdata/import-testdata.sh
# 可用覆盖变量：
#   CUSTOMER_MYSQL_CONTAINER / PORTAL_MYSQL_CONTAINER / CUSTOMER_API_CONTAINER / PORTAL_API_CONTAINER

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
customer_mysql="${CUSTOMER_MYSQL_CONTAINER:-basic-platform-local-customer-mysql-1}"
portal_mysql="${PORTAL_MYSQL_CONTAINER:-basic-platform-local-portal-mysql-1}"
customer_api="${CUSTOMER_API_CONTAINER:-basic-platform-local-customer-api-1}"
portal_api="${PORTAL_API_CONTAINER:-basic-platform-local-portal-api-1}"

for c in "$customer_mysql" "$portal_mysql" "$customer_api" "$portal_api"; do
  docker inspect "$c" >/dev/null 2>&1 || { echo "找不到容器：$c（可设置 CUSTOMER_MYSQL_CONTAINER 等变量）" >&2; exit 1; }
done

dsn_of() {
  local container="$1" key="$2"
  docker inspect "$container" --format '{{range .Config.Env}}{{println .}}{{end}}' \
    | awk -F= -v key="$key" '$1 == key { print substr($0, index($0,"=")+1); exit }'
}

run_sql() {
  local container="$1" dsn="$2" label="$3" file="$4"
  local user db pass
  user="$(printf '%s' "$dsn" | sed -E 's#^([^:]+):.*#\1#')"
  pass="$(printf '%s' "$dsn" | sed -E 's#^[^:]+:([^@]*)@.*#\1#')"
  db="$(printf '%s' "$dsn" | sed -E 's#.*/([^?]+).*#\1#')"
  echo ">>> 导入 ${label}: ${file}"
  docker exec -i "$container" sh -c \
    "MYSQL_PWD='$pass' mysql --default-character-set=utf8mb4 -h127.0.0.1 -u'$user' '$db'" < "$file"
}

customer_dsn="$(dsn_of "$customer_api" MYSQL_DSN)"
portal_dsn="$(dsn_of "$portal_api" PORTAL_MYSQL_DSN)"
[[ -n "$customer_dsn" && -n "$portal_dsn" ]] || { echo "无法从容器读取 MySQL DSN" >&2; exit 1; }

run_sql "$customer_mysql" "$customer_dsn" "CRM 核心" "$script_dir/01_crm_core.sql"
run_sql "$customer_mysql" "$customer_dsn" "售前 TS" "$script_dir/02_crm_presale.sql"
run_sql "$customer_mysql" "$customer_dsn" "预警/通知/Portal 邀请" "$script_dir/03_crm_alerts_portal.sql"
run_sql "$portal_mysql" "$portal_dsn" "客户门户" "$script_dir/04_portal.sql"

echo "测试数据导入完成。"

#!/usr/bin/env bash
set -Eeuo pipefail

# 导入客户与商机管理测试数据到本地 docker 的 MySQL。
# 用法：bash scripts/testdata/import-testdata.sh
# 可用覆盖变量：
#   CUSTOMER_MYSQL_CONTAINER / PORTAL_MYSQL_CONTAINER / CUSTOMER_API_CONTAINER / PORTAL_API_CONTAINER
#   PLATFORM_MYSQL_CONTAINER / CRM_TEST_OWNER_1..5（格式 user_id:org_id，缺省自动从平台 CRM 授权目录解析）

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
customer_mysql="${CUSTOMER_MYSQL_CONTAINER:-basic-platform-local-customer-mysql-1}"
portal_mysql="${PORTAL_MYSQL_CONTAINER:-basic-platform-local-portal-mysql-1}"
customer_api="${CUSTOMER_API_CONTAINER:-basic-platform-local-customer-api-1}"
portal_api="${PORTAL_API_CONTAINER:-basic-platform-local-portal-api-1}"
platform_mysql="${PLATFORM_MYSQL_CONTAINER:-basic-platform-local-mysql-1}"

for c in "$customer_mysql" "$portal_mysql" "$customer_api" "$portal_api" "$platform_mysql"; do
  docker inspect "$c" >/dev/null 2>&1 || { echo "找不到容器：$c（可设置 CUSTOMER_MYSQL_CONTAINER 等变量）" >&2; exit 1; }
done

dsn_of() {
  local container="$1" key="$2"
  docker inspect "$container" --format '{{range .Config.Env}}{{println .}}{{end}}' \
    | awk -F= -v key="$key" '$1 == key { print substr($0, index($0,"=")+1); exit }'
}

run_sql() {
  local container="$1" dsn="$2" label="$3" sed_program="$4" file="$5"
  local user db pass
  user="$(printf '%s' "$dsn" | sed -E 's#^([^:]+):.*#\1#')"
  pass="$(printf '%s' "$dsn" | sed -E 's#^[^:]+:([^@]*)@.*#\1#')"
  db="$(printf '%s' "$dsn" | sed -E 's#.*/([^?]+).*#\1#')"
  echo ">>> 导入 ${label}: ${file}"
  sed -e "$sed_program" "$file" | docker exec -i "$container" sh -c \
    "MYSQL_PWD='$pass' mysql --default-character-set=utf8mb4 -h127.0.0.1 -u'$user' '$db'"
}

customer_dsn="$(dsn_of "$customer_api" MYSQL_DSN)"
portal_dsn="$(dsn_of "$portal_api" PORTAL_MYSQL_DSN)"
[[ -n "$customer_dsn" && -n "$portal_dsn" ]] || { echo "无法从容器读取 MySQL DSN" >&2; exit 1; }

# 演示数据里的 01KYDVHC... 是占位人员，不是平台真实用户；导入前必须替换成平台真实用户，
# 否则人员目录解析不出姓名，前端负责人会退化为显示用户 ID。
platform_root_password="$(dsn_of "$platform_mysql" MYSQL_ROOT_PASSWORD)"
[[ -n "$platform_root_password" ]] || { echo "无法从平台 MySQL 容器读取 MYSQL_ROOT_PASSWORD" >&2; exit 1; }

platform_owner_row() {
  docker exec "$platform_mysql" sh -c \
    "MYSQL_PWD='$platform_root_password' mysql --default-character-set=utf8mb4 -h127.0.0.1 -uroot -D basic_platform -Nse \"$1\""
}

resolve_owner() {
  local index="$1" override
  override="$(eval "printf '%s' \"\${CRM_TEST_OWNER_${index}:-}\"")"
  if [[ -n "$override" ]]; then
    printf '%s' "$override"
    return
  fi
  platform_owner_row "
    SELECT u.id, m.org_unit_id
    FROM iam_user u
    JOIN iam_membership m ON m.tenant_id=u.tenant_id AND m.user_id=u.id AND m.status='ACTIVE'
    WHERE u.tenant_id='01J00000000000000000000000' AND u.status='ACTIVE' AND u.deleted_at IS NULL AND u.employment_status <> 'EXTERNAL_CUSTOMER'
    AND EXISTS (
      SELECT 1 FROM authz_role_binding b
      JOIN authz_role r ON r.id=b.role_id AND r.tenant_id=b.tenant_id AND r.application_id=b.application_id
      JOIN platform_application a ON a.id=r.application_id
      WHERE b.tenant_id=u.tenant_id AND a.code='customer_and_opportunity' AND b.status='ACTIVE' AND r.status='ACTIVE' AND r.role_type <> 'COMPATIBILITY'
      AND (b.valid_from IS NULL OR b.valid_from<=NOW()) AND (b.valid_until IS NULL OR b.valid_until>NOW())
      AND ((b.scope_type='TENANT' AND b.scope_id='') OR (b.scope_type='ENVIRONMENT' AND b.scope_id IN (SELECT e.id FROM platform_application_environment e WHERE e.application_id=a.id AND e.environment='dev' AND e.status='ACTIVE')))
      AND ((b.subject_type='USER' AND b.subject_id=u.id) OR (m.inherit_authorization=1 AND b.subject_type='ORG_UNIT' AND b.subject_id=m.org_unit_id) OR (m.inherit_authorization=1 AND b.subject_type='POSITION' AND b.subject_id=m.position_id AND EXISTS(SELECT 1 FROM iam_position p WHERE p.id=m.position_id AND p.org_unit_id=m.org_unit_id AND p.status='ACTIVE')))
    )
    ORDER BY u.created_at, u.id LIMIT 1 OFFSET $((index - 1))" \
    | awk '{print $1 ":" $2}'
}

owner_sed=""
declare -a available_owners
for index in 1 2 3 4 5; do
  value="$(resolve_owner "$index")"
  if [[ -z "$value" ]]; then
    break
  fi
  available_owners[$index]="$value"
done
if [[ "${#available_owners[@]}" -eq 0 ]]; then
  echo "平台 CRM 授权目录没有可用用户；请先在平台为测试人员分配 CRM 角色与组织任职，或设置 CRM_TEST_OWNER_1..5（user_id:org_id）" >&2
  exit 1
fi

fake_user_ids=(01KYDVHC00000000000000000C 01KYDVHC00000000000000000D 01KYDVHC00000000000000000E 01KYDVHC00000000000000000F 01KYDVHC00000000000000000G)
fake_org_ids=(01KYDVHC000000000000000002 01KYDVHC000000000000000003 01KYDVHC000000000000000005 01KYDVHC000000000000000006)
for index in 1 2 3 4 5; do
  available_count="${#available_owners[@]}"
  owner_index=$((((index - 1) % available_count) + 1))
  user_id="${available_owners[$owner_index]%%:*}"
  org_id="${available_owners[$owner_index]##*:}"
  owner_sed+="s/${fake_user_ids[$((index - 1))]}/${user_id}/g;"
  if [[ $index -le 4 ]]; then
    owner_sed+="s/${fake_org_ids[$((index - 1))]}/${org_id}/g;"
  fi
done

run_sql "$customer_mysql" "$customer_dsn" "CRM 核心" "$owner_sed" "$script_dir/01_crm_core.sql"
run_sql "$customer_mysql" "$customer_dsn" "售前 TS" "$owner_sed" "$script_dir/02_crm_presale.sql"
run_sql "$customer_mysql" "$customer_dsn" "预警/通知/Portal 邀请" "$owner_sed" "$script_dir/03_crm_alerts_portal.sql"
run_sql "$portal_mysql" "$portal_dsn" "客户门户" "$owner_sed" "$script_dir/04_portal.sql"

echo "测试数据导入完成。"

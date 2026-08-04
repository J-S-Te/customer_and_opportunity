#!/usr/bin/env bash
set -Eeuo pipefail

# 用 JSON 请求体在 docker 内执行客户与商机测试数据导入。
#
# 原理：
#   1. 从已运行的 customer-api 容器复制环境，启动一个临时的、只对本机开放的
#      DEV_AUTH_ENABLED=true CRM 容器（不修改正在运行的业务容器）；
#   2. 用 X-Dev-* 请求头 + wget 调用真实 HTTP API，完成客户/商机/跟进/阶段/售前申请；
#   3. 执行完自动删除临时容器。
#
# 服务器用法：
#   DEPLOY_PATH=/opt/basic-platform bash scripts/testdata/run-json-in-docker.sh
# 本地用法：
#   bash scripts/testdata/run-json-in-docker.sh

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
json_dir="$script_dir/json"
seed_name="${CRM_SEED_CONTAINER:-crm-json-seed}"
api_container="${CUSTOMER_API_CONTAINER:-basic-platform-local-customer-api-1}"
seed_port="${CRM_SEED_PORT:-18092}"
tenant="${CRM_TEST_TENANT:-01J00000000000000000000000}"
actor="${CRM_TEST_ACTOR:-oidc-sub-demo-seed}"
perms="${CRM_TEST_PERMISSIONS:-customer.create,customer.duplicate.override,customer.update,customer.followup.create,opportunity.create,opportunity.update,opportunity.stage.change,opportunity.team.manage,presale.create,presale.read,presale.assign,presale.progress,presale.worklog}"
key_prefix="${CRM_TEST_KEY_PREFIX:-crm-json}"

deploy_path="${DEPLOY_PATH:-/opt/basic-platform}"
if [[ "$api_container" == basic-platform-local-* ]]; then
  keys_dir="$script_dir/../../../platform/data/keys"
else
  keys_dir="$deploy_path/data/platform/keys"
fi

docker inspect "$api_container" >/dev/null 2>&1 || { echo "找不到容器：$api_container" >&2; exit 1; }
[[ -f "$keys_dir/jwt-ed25519-public.pem" ]] || { echo "找不到密钥：$keys_dir/jwt-ed25519-public.pem" >&2; exit 1; }

image="$(docker inspect -f '{{.Config.Image}}' "$api_container")"
network="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$api_container")"
tmp_env="$(mktemp)"

cleanup() {
  local code=$?
  trap - EXIT INT TERM
  docker rm -f "$seed_name" >/dev/null 2>&1 || true
  rm -f "$tmp_env"
  exit "$code"
}
trap cleanup EXIT INT TERM

docker rm -f "$seed_name" >/dev/null 2>&1 || true
docker inspect "$api_container" --format '{{range .Config.Env}}{{println .}}{{end}}' > "$tmp_env"
echo ">>> 启动临时 dev-auth CRM 容器：$seed_name"
docker run -d --name "$seed_name" --network "$network" \
  --env-file "$tmp_env" \
  -e DEV_AUTH_ENABLED=true \
  -e HTTP_ADDRESS=":$seed_port" \
  -e PLATFORM_AUTHORIZATION_CATALOG_SYNC_ENABLED=false \
  -v "$keys_dir:/app/data/keys:ro" \
  "$image" ./crm-server >/dev/null

ready=false
for _ in $(seq 1 60); do
  if docker exec "$seed_name" sh -c "wget -qO- --timeout=2 http://127.0.0.1:$seed_port/customer-opportunity/healthz" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[[ "$ready" == true ]] || { echo "临时 CRM 容器健康检查超时" >&2; exit 1; }

docker exec "$seed_name" sh -c 'apk add --no-cache curl jq >/dev/null 2>&1' || true

CUSTOMER_ID_1=""
CUSTOMER_ID_2=""
CUSTOMER_ID_3=""
OPPORTUNITY_ID_1=""
OPPORTUNITY_ID_2=""
OPPORTUNITY_ID_3=""
PRESALE_ID_1=""

fill() {
  local file="$1"
  sed -e "s/{{CUSTOMER_ID_1}}/${CUSTOMER_ID_1}/g" \
      -e "s/{{CUSTOMER_ID_2}}/${CUSTOMER_ID_2}/g" \
      -e "s/{{CUSTOMER_ID_3}}/${CUSTOMER_ID_3}/g" \
      -e "s/{{OPPORTUNITY_ID_1}}/${OPPORTUNITY_ID_1}/g" \
      -e "s/{{OPPORTUNITY_ID_2}}/${OPPORTUNITY_ID_2}/g" \
      -e "s/{{OPPORTUNITY_ID_3}}/${OPPORTUNITY_ID_3}/g" \
      "$file"
}

post() {
  local path="$1" idem="$2" file="$3" outvar="$4"
  local tmp resp id b64
  tmp="$(mktemp)"
  fill "$file" > "$tmp"
  b64="$(base64 < "$tmp")"
  resp="$(docker exec "$seed_name" sh -c \
    "printf '%s' '$b64' | base64 -d > /tmp/seed-body.json; curl -sS --max-time 30 \
      -H 'Idempotency-Key: $idem' \
      -H 'X-Dev-User-ID: $actor' \
      -H 'X-Dev-Tenant-ID: $tenant' \
      -H 'X-Dev-Display-Name: JSON测试导入' \
      -H 'X-Dev-Permissions: $perms' \
      -H 'X-Dev-Roles: sales_director' \
      -H 'X-Dev-Scope: ALL' \
      --data-binary @/tmp/seed-body.json \
      http://127.0.0.1:$seed_port/customer-opportunity/api/v1$path" || true)"
  echo ">>> POST $path"
  printf '%s\n' "$resp" | sed -n '1,20p'
  id="$(printf '%s' "$resp" | docker exec -i "$seed_name" jq -r '.data.id // empty' 2>/dev/null || true)"
  if [[ -z "$id" || "$id" == "null" ]]; then
    id="$(printf '%s' "$resp" | docker exec -i "$seed_name" jq -r '.details[]? | select(.exact_code == true) | .id // empty' 2>/dev/null | head -n1 || true)"
  fi
  if [[ -n "$id" && "$id" != "null" ]]; then
    printf -v "$outvar" '%s' "$id"
  fi
  rm -f "$tmp"
}

post "/customers" "$key_prefix-customer-01" "$json_dir/customer-01.json" CUSTOMER_ID_1
post "/customers" "$key_prefix-customer-02" "$json_dir/customer-02.json" CUSTOMER_ID_2
post "/customers" "$key_prefix-customer-03" "$json_dir/customer-03.json" CUSTOMER_ID_3
post "/opportunities" "$key_prefix-opportunity-01" "$json_dir/opportunity-01.json" OPPORTUNITY_ID_1
post "/opportunities" "$key_prefix-opportunity-02" "$json_dir/opportunity-02.json" OPPORTUNITY_ID_2
post "/opportunities" "$key_prefix-opportunity-03" "$json_dir/opportunity-03.json" OPPORTUNITY_ID_3

if [[ -n "$OPPORTUNITY_ID_1" ]]; then
  post "/opportunities/$OPPORTUNITY_ID_1/followups" "$key_prefix-followup-op-01" "$json_dir/followup-opportunity-01.json" FOLLOWUP_ID_1
  post "/opportunities/$OPPORTUNITY_ID_1/stage-changes" "$key_prefix-stage-01" "$json_dir/stage-change-01.json" STAGE_ID_1
  post "/presale/requests" "$key_prefix-presale-01" "$json_dir/presale-01.json" PRESALE_ID_1
fi

echo
echo "========== 导入结果 =========="
echo "CUSTOMER_ID_1=$CUSTOMER_ID_1 CUSTOMER_ID_2=$CUSTOMER_ID_2 CUSTOMER_ID_3=$CUSTOMER_ID_3"
echo "OPPORTUNITY_ID_1=$OPPORTUNITY_ID_1 OPPORTUNITY_ID_2=$OPPORTUNITY_ID_2 OPPORTUNITY_ID_3=$OPPORTUNITY_ID_3"
echo "PRESALE_ID_1=$PRESALE_ID_1"
echo "临时容器 $seed_name 已自动删除。"

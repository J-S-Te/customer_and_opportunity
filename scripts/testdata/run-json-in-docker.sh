#!/usr/bin/env bash
set -Eeuo pipefail

echo "此脚本原先依赖已删除的请求头开发身份，现已安全停用。" >&2
echo "请通过基础平台 OIDC 登录 CRM 后，使用受审计的正式 API 或受控导入功能写入测试数据。" >&2
exit 2

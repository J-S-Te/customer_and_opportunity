-- AUD-IP-2026：与 000113_request_audit_ip_provenance 对齐，Portal 请求审计 Outbox
-- 同样记录统一来源协议的三类地址。user_login_ip 保留为登录地址（历史语义不变）；
-- client_ip 是本次操作地址；ip_source 记录获取方式，历史行为空时按 UNKNOWN 处理。
-- portal-server 的 requestaudit Store 对该表写入 client_ip/ip_source，缺列时
-- 审计中间件 fail-closed 并拒绝全部 Portal HTTP 请求。
ALTER TABLE portal_application_request_audit_outbox
    ADD COLUMN client_ip VARCHAR(45) NOT NULL DEFAULT '' AFTER user_login_ip,
    ADD COLUMN ip_source VARCHAR(32) NOT NULL DEFAULT 'UNKNOWN' AFTER client_ip;

-- AUD-IP-2026：请求审计 Outbox 记录统一来源协议的三类地址。
-- user_login_ip 保留为登录地址（历史语义不变）；client_ip 是本次操作地址；
-- ip_source 记录获取方式，历史行为空时按 UNKNOWN 处理。
ALTER TABLE application_request_audit_outbox
    ADD COLUMN client_ip VARCHAR(45) NOT NULL DEFAULT '' AFTER user_login_ip,
    ADD COLUMN ip_source VARCHAR(32) NOT NULL DEFAULT 'UNKNOWN' AFTER client_ip;

-- 生产回滚前需先确认没有客户依赖已关闭的服务项。
DROP TABLE IF EXISTS portal_customer_service_options;

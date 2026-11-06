-- 本地身份及来源锁已有业务使用后不能通过回滚自动转换为CRM权威。
-- 只能使用经批准的数据迁移计划；禁止丢弃客户、邀请、操作与文件来源台账。
SIGNAL SQLSTATE '45000'
 SET MESSAGE_TEXT = 'portal local identity is forward-only; preserve authority and business ledgers';

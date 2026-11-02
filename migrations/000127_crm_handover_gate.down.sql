-- 回滚：门闩表只是冻结/责任写入的串行化辅助，删除不影响既有业务数据。
DROP TABLE IF EXISTS crm_handover_gate;

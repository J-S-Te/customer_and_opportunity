-- =============================================================
-- 客户与商机管理子系统 — 数据库建表 DDL (T3)
-- 数据库: MySQL 8.0 (InnoDB, utf8mb4)
-- 来源: 数据字典 V1.0 + ER 图 V1.0
-- 约定:
--   1) 主键沿用业务编号 VARCHAR(20)，应用层生成 KH/SJ/BJ+YYYYMMDD+4位流水
--   2) 所有表统一 created_time / updated_time (ORM 维护)
--   3) 软删除用 status/is_archived + end_date，禁止物理 DELETE
--   4) 敏感字段(phone/bank_account)由应用层 AES-256 加密存储
--   5) 多态附件(t_attachment) biz_type+biz_id 联合索引，不建外键
--   6) JSON 字段: competitor_info/preference_tags/support_team/
--      reminder_channels/project_ids/ip_whitelist/clarification_files/attachment_ids
-- 说明: 先建全部表(无外键)，末尾 ALTER ADD CONSTRAINT 补外键，
--       级联策略严格遵循 ER 图 V1.0 §3。
-- =============================================================

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

-- ===================== 1. 客户主数据 =====================

CREATE TABLE t_customer (
  customer_id        VARCHAR(20)   NOT NULL COMMENT '客户编号 PK',
  customer_name      VARCHAR(200)  NOT NULL COMMENT '客户名称 查重字段1(模糊)',
  customer_type      ENUM('业主','三方','其他','政府','事业单位','个人') NOT NULL COMMENT '客户类型',
  credit_code        VARCHAR(18)   NOT NULL COMMENT '统一社会信用代码 查重字段2(精确)',
  industry           ENUM('金融','政府','能源','制造','电信','医疗','教育','交通','互联网','其他') NOT NULL COMMENT '所属行业',
  enterprise_scale   ENUM('大','中','小','微') NULL COMMENT '企业规模(工信部划型)',
  reg_address        VARCHAR(500)  NOT NULL COMMENT '注册地址',
  office_address     VARCHAR(500)  NULL COMMENT '办公地址',
  customer_status    ENUM('潜在','在跟','成交','流失','作废') NOT NULL DEFAULT '潜在' COMMENT '客户状态 作废=软删',
  credit_level       ENUM('A','B','C','D') NOT NULL COMMENT '信用等级 CM-003',
  credit_score       SMALLINT      NOT NULL COMMENT '信用评分 0~100',
  risk_flag          TINYINT(1)    NOT NULL DEFAULT 0 COMMENT '风险标识 true=D级强预警',
  is_merged          TINYINT(1)    NOT NULL DEFAULT 0 COMMENT '是否合并客户',
  merged_to          VARCHAR(20)   NULL COMMENT '合并目标客户ID 自关联',
  end_date           DATE          NULL COMMENT '业务结束日期 6年保留期起算',
  created_by         VARCHAR(50)   NOT NULL COMMENT '创建人',
  created_time       DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_by         VARCHAR(50)   NOT NULL COMMENT '更新人',
  updated_time       DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (customer_id),
  KEY idx_dedup (customer_name, credit_code),
  KEY idx_status_level (customer_status, credit_level),
  KEY idx_merged (is_merged, merged_to),
  KEY idx_end_date (end_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户档案主表';

CREATE TABLE t_customer_biz (
  biz_id             VARCHAR(20)   NOT NULL COMMENT '业务ID PK',
  customer_id        VARCHAR(20)   NOT NULL COMMENT '所属客户',
  main_business      TEXT          NOT NULL COMMENT '主营业务',
  test_demand_type   JSON          NOT NULL COMMENT '测评需求类型(多选数组)',
  special_requirement TEXT         NULL COMMENT '特殊要求',
  cooperation_history TEXT         NULL COMMENT '历史合作项目摘要',
  created_time       DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time       DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (biz_id),
  KEY idx_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户业务信息表';

CREATE TABLE t_customer_org (
  org_id           VARCHAR(20)   NOT NULL COMMENT '组织ID PK',
  customer_id      VARCHAR(20)   NOT NULL COMMENT '所属客户',
  dept_name        VARCHAR(100)  NOT NULL COMMENT '部门名称',
  decision_level   TINYINT       NOT NULL COMMENT '决策层级 1~5',
  key_position     VARCHAR(100)  NOT NULL COMMENT '关键岗位',
  created_time     DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time     DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (org_id),
  KEY idx_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户组织架构表';

CREATE TABLE t_customer_stakeholder (
  stakeholder_id    VARCHAR(20)   NOT NULL COMMENT '干系人ID PK',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '所属客户',
  name              VARCHAR(50)   NOT NULL COMMENT '姓名',
  position          VARCHAR(100)  NOT NULL COMMENT '职务',
  phone             VARCHAR(20)   NOT NULL COMMENT '联系电话(脱敏)',
  email             VARCHAR(100)  NULL COMMENT '邮箱',
  decision_weight   TINYINT       NOT NULL COMMENT '决策权重 1~5',
  preference_tags   JSON          NULL COMMENT '偏好标签数组',
  last_communicate_time DATETIME  NULL COMMENT '最近沟通时间',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (stakeholder_id),
  KEY idx_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户干系人表';

CREATE TABLE t_customer_system (
  biz_system_id     VARCHAR(20)   NOT NULL COMMENT '系统清单ID PK',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '所属客户',
  system_name       VARCHAR(200)  NOT NULL COMMENT '系统名称',
  system_level      ENUM('一级','二级','三级','四级','五级') NOT NULL COMMENT '系统等级(等保级别)',
  deploy_mode       ENUM('公有云','私有云','混合云','本地','其他') NOT NULL COMMENT '部署方式',
  system_count      INT           NOT NULL COMMENT '数量 >=1',
  test_history      TEXT          NULL COMMENT '测评历史',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (biz_system_id),
  KEY idx_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户系统清单表';

CREATE TABLE t_customer_finance (
  customer_id       VARCHAR(20)   NOT NULL COMMENT '所属客户 PK/FK 1:1',
  bank_account      VARCHAR(50)   NOT NULL COMMENT '收款账户(脱敏)',
  account_name      VARCHAR(100)  NOT NULL COMMENT '开户名',
  bank_name         VARCHAR(200)  NOT NULL COMMENT '开户行',
  invoice_title     VARCHAR(200)  NOT NULL COMMENT '开票抬头',
  tax_no            VARCHAR(50)   NOT NULL COMMENT '税号',
  invoice_address   VARCHAR(500)  NULL COMMENT '开票地址电话',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户财务信息表';

CREATE TABLE t_customer_log (
  log_id            VARCHAR(20)   NOT NULL COMMENT '日志ID PK',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '客户ID',
  op_type           ENUM('新增','编辑','删除','合并','查重','信用调整','状态变更') NOT NULL COMMENT '操作类型',
  op_user           VARCHAR(50)   NOT NULL COMMENT '操作人',
  op_time           DATETIME      NOT NULL COMMENT '操作时间',
  change_content    TEXT          NOT NULL COMMENT '变更内容 字段:旧值→新值',
  op_reason         TEXT          NULL COMMENT '操作原因(合并/作废/信用调整必填)',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (log_id),
  KEY idx_customer_time (customer_id, op_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户操作日志表(永久留存)';

CREATE TABLE t_customer_communication (
  comm_id           VARCHAR(20)   NOT NULL COMMENT '沟通记录ID PK',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '客户ID',
  comm_time         DATETIME      NOT NULL COMMENT '沟通时间',
  comm_type         ENUM('上门拜访','电话','邮件','会议','IM') NOT NULL COMMENT '沟通方式',
  stakeholder_id    VARCHAR(20)   NOT NULL COMMENT '干系人',
  content           LONGTEXT      NOT NULL COMMENT '沟通内容(富文本)',
  feedback          TEXT          NULL COMMENT '客户反馈',
  next_step         TEXT          NULL COMMENT '下一步计划',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (comm_id),
  KEY idx_customer_time (customer_id, comm_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户沟通记录表(永久留存)';

-- ===================== 2. 商机主数据 =====================

CREATE TABLE t_business_opportunity (
  opportunity_id    VARCHAR(20)   NOT NULL COMMENT '商机编号 PK',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '关联客户',
  opp_name          VARCHAR(200)  NOT NULL COMMENT '商机名称',
  opp_source        ENUM('老客户推荐','公开招标','自主开发','客户介绍','行业活动','其他') NOT NULL COMMENT '商机来源',
  opp_type          ENUM('等保测评','风险评估','渗透测试','代码审计','安全咨询','综合') NOT NULL COMMENT '商机类型',
  expected_amount   DECIMAL(15,2) NOT NULL COMMENT '预计金额 >0',
  expected_sign_date DATE         NOT NULL COMMENT '预计签单时间 >今日',
  current_stage     ENUM('初步接触','需求沟通','方案制定','报价','投标') NOT NULL COMMENT '当前阶段(支持回退)',
  opp_status        ENUM('跟进中','已签单','已流失','已作废') NOT NULL COMMENT '商机状态',
  competitor_info   JSON          NULL COMMENT '竞争对手信息(多对手)',
  competitor_strategy TEXT        NULL COMMENT '应对策略',
  sales_owner       VARCHAR(50)   NOT NULL COMMENT '销售负责人',
  support_team      JSON          NULL COMMENT '支持人员数组',
  lost_reason       ENUM('价格','技术','关系','客户预算','竞争对手','其他') NULL COMMENT '流失原因',
  lost_type         ENUM('暂时流失','永久流失') NULL COMMENT '流失类型',
  is_archived       TINYINT(1)    NOT NULL DEFAULT 0 COMMENT '是否归档',
  end_date          DATE          NULL COMMENT '业务结束日期 6年保留期起算',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (opportunity_id),
  KEY idx_customer_stage (customer_id, current_stage),
  KEY idx_status_archived (opp_status, is_archived),
  KEY idx_owner_sign (sales_owner, expected_sign_date),
  KEY idx_end_date (end_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='商机主表';

CREATE TABLE t_opportunity_follow (
  follow_id         VARCHAR(20)   NOT NULL COMMENT '跟进ID PK',
  opportunity_id    VARCHAR(20)   NOT NULL COMMENT '关联商机',
  follow_time       DATETIME      NOT NULL COMMENT '跟进时间',
  follow_type       ENUM('上门拜访','电话','邮件','会议','IM') NOT NULL COMMENT '跟进方式',
  stage_before      ENUM('初步接触','需求沟通','方案制定','报价','投标') NOT NULL COMMENT '推进前阶段',
  stage_after       ENUM('初步接触','需求沟通','方案制定','报价','投标') NOT NULL COMMENT '推进后阶段',
  content           LONGTEXT      NOT NULL COMMENT '沟通内容(富文本)',
  customer_feedback TEXT          NULL COMMENT '客户反馈',
  next_follow_time  DATETIME      NULL COMMENT '下次跟进时间',
  reminder_channels JSON          NULL COMMENT '提醒渠道(本期仅站内)',
  key_result        TEXT          NULL COMMENT '关键成果(阶段推进必填)',
  attachment_ids    JSON          NULL COMMENT '附件(关联t_attachment)',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (follow_id),
  KEY idx_opportunity (opportunity_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='商机跟进记录表';

CREATE TABLE t_opportunity_approval (
  approval_id       VARCHAR(20)   NOT NULL COMMENT '审批ID PK',
  opportunity_id    VARCHAR(20)   NOT NULL COMMENT '关联商机',
  approval_type     ENUM('阶段推进','作废') NOT NULL COMMENT '审批类型',
  from_stage        ENUM('初步接触','需求沟通','方案制定','报价','投标') NULL COMMENT '原阶段',
  to_stage          ENUM('初步接触','需求沟通','方案制定','报价','投标') NULL COMMENT '目标阶段',
  submit_user       VARCHAR(50)   NOT NULL COMMENT '提交人',
  submit_time       DATETIME      NOT NULL COMMENT '提交时间',
  approver_user     VARCHAR(50)   NOT NULL COMMENT '审批人(销售总监必经)',
  approve_time      DATETIME      NULL COMMENT '审批时间',
  approve_status    ENUM('待审批','通过','驳回') NOT NULL COMMENT '审批状态',
  approve_opinion   TEXT          NULL COMMENT '审批意见',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (approval_id),
  KEY idx_opportunity (opportunity_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='商机审批表';

-- ===================== 3. 报价管理 =====================

CREATE TABLE t_quotation (
  quotation_id      VARCHAR(20)   NOT NULL COMMENT '报价单号 PK',
  opportunity_id    VARCHAR(20)   NOT NULL COMMENT '关联商机',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '关联客户(冗余)',
  quotation_date    DATE          NOT NULL COMMENT '报价日期 <=今日',
  effective_end_date DATE         NOT NULL COMMENT '有效期至 默认+30天',
  subtotal          DECIMAL(15,2) NOT NULL COMMENT '小计',
  discount_amount   DECIMAL(15,2) NOT NULL COMMENT '折扣金额',
  tax_amount        DECIMAL(15,2) NOT NULL COMMENT '税费',
  total_amount      DECIMAL(15,2) NOT NULL COMMENT '总金额',
  payment_terms_default TINYINT(1) NOT NULL COMMENT '默认付款条款标识',
  service_period    VARCHAR(200)  NOT NULL COMMENT '服务周期',
  warranty_terms    TEXT          NULL COMMENT '质保条款',
  special_terms     TEXT          NULL COMMENT '特殊约定',
  quotation_status  ENUM('草稿','审批中','已生效','已失效','已转合同') NOT NULL COMMENT '报价单状态',
  version           INT           NOT NULL DEFAULT 1 COMMENT '版本号',
  approval_flow_id  VARCHAR(20)   NOT NULL COMMENT '审批流程实例ID',
  below_price_flag  TINYINT(1)    NOT NULL COMMENT '低于限价标识',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (quotation_id),
  KEY idx_opp_version (opportunity_id, version),
  KEY idx_status_eff (quotation_status, effective_end_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='报价单主表';

CREATE TABLE t_quotation_item (
  item_id           VARCHAR(20)   NOT NULL COMMENT '明细ID PK',
  quotation_id      VARCHAR(20)   NOT NULL COMMENT '关联报价单',
  service_item      VARCHAR(200)  NOT NULL COMMENT '服务项目(价目表)',
  service_content   TEXT          NOT NULL COMMENT '服务内容',
  quantity          INT           NOT NULL COMMENT '数量 >=1',
  unit_price        DECIMAL(15,2) NOT NULL COMMENT '单价',
  discount_rate     DECIMAL(5,4)  NOT NULL COMMENT '折扣率 0~1',
  amount            DECIMAL(15,2) NOT NULL COMMENT '金额',
  sort_order        INT           NOT NULL DEFAULT 1 COMMENT '排序(对齐ER索引建议)',
  remark            TEXT          NULL COMMENT '备注',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (item_id),
  KEY idx_quotation (quotation_id, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='报价明细表';

CREATE TABLE t_quotation_payment_term (
  term_id           VARCHAR(20)   NOT NULL COMMENT '条款ID PK',
  quotation_id      VARCHAR(20)   NOT NULL COMMENT '关联报价单',
  phase_name        VARCHAR(50)   NOT NULL COMMENT '阶段名称',
  pay_ratio         DECIMAL(5,4)  NOT NULL COMMENT '付款比例 0~1',
  condition_desc    TEXT          NOT NULL COMMENT '触发条件说明',
  sort_order        INT           NOT NULL DEFAULT 1 COMMENT '排序',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (term_id),
  KEY idx_quotation (quotation_id, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='报价付款条款表';

CREATE TABLE t_quotation_approval (
  approval_id       VARCHAR(20)   NOT NULL COMMENT '审批ID PK',
  quotation_id      VARCHAR(20)   NOT NULL COMMENT '关联报价单',
  node_order        INT           NOT NULL COMMENT '节点顺序 1=销售总监 2=财务经理',
  approver_user     VARCHAR(50)   NOT NULL COMMENT '审批人',
  approve_status    ENUM('待审批','通过','驳回') NOT NULL COMMENT '审批状态',
  approve_time      DATETIME      NULL COMMENT '审批时间',
  approve_opinion   TEXT          NULL COMMENT '审批意见',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (approval_id),
  KEY idx_quotation (quotation_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='报价审批节点表';

-- ===================== 4. 投标管理 =====================

CREATE TABLE t_bid_project (
  bid_id            VARCHAR(20)   NOT NULL COMMENT '投标ID PK',
  opportunity_id    VARCHAR(20)   NOT NULL COMMENT '关联商机',
  project_name      VARCHAR(300)  NOT NULL COMMENT '项目名称',
  bid_code          VARCHAR(100)  NOT NULL COMMENT '招标编号',
  tender_name       VARCHAR(200)  NOT NULL COMMENT '招标人',
  agency_name       VARCHAR(200)  NOT NULL COMMENT '代理机构',
  bid_deadline      DATETIME      NOT NULL COMMENT '投标截止时间 >今日',
  bid_status        ENUM('准备中','标书制作','已投标','中标','落标') NOT NULL COMMENT '投标状态',
  winning_notice    VARCHAR(500)  NULL COMMENT '中标通知书(文件)',
  bid_result_time   DATETIME      NULL COMMENT '开标时间',
  bid_rank          INT           NULL COMMENT '排名',
  lost_reason       TEXT          NULL COMMENT '落标原因',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (bid_id),
  KEY idx_opp_status (opportunity_id, bid_status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='投标项目主表';

CREATE TABLE t_bid_document (
  doc_id            VARCHAR(20)   NOT NULL COMMENT '标书ID PK',
  bid_id            VARCHAR(20)   NOT NULL COMMENT '关联投标',
  purchase_time     DATETIME      NOT NULL COMMENT '购买时间',
  purchase_amount   DECIMAL(15,2) NOT NULL COMMENT '购买金额',
  document_file     VARCHAR(500)  NOT NULL COMMENT '标书文件',
  answer_records    TEXT          NULL COMMENT '答疑记录',
  clarification_files JSON        NULL COMMENT '澄清文件数组',
  version           INT           NOT NULL COMMENT '版本号(多人协同自增)',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (doc_id),
  KEY idx_bid (bid_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='标书信息表';

CREATE TABLE t_bid_deposit (
  deposit_id        VARCHAR(20)   NOT NULL COMMENT '保证金ID PK',
  bid_id            VARCHAR(20)   NOT NULL COMMENT '关联投标',
  amount            DECIMAL(15,2) NOT NULL COMMENT '金额 >0',
  pay_time          DATETIME      NOT NULL COMMENT '缴纳时间',
  pay_voucher       VARCHAR(500)  NOT NULL COMMENT '缴纳凭证',
  refund_status     ENUM('未退','已退','部分退') NOT NULL COMMENT '退还状态',
  refund_amount     DECIMAL(15,2) NULL COMMENT '退还金额',
  refund_time       DATETIME      NULL COMMENT '退还时间',
  expected_refund_date DATE       NOT NULL COMMENT '预计到期时间(用户自定义)',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (deposit_id),
  KEY idx_refund (refund_status, expected_refund_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='投标保证金表';

-- ===================== 5. 客户自助门户 =====================

CREATE TABLE t_portal_account (
  account_id        VARCHAR(20)   NOT NULL COMMENT '账号ID PK',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '关联客户',
  login_name        VARCHAR(50)   NOT NULL COMMENT '登录名 唯一',
  password_hash     VARCHAR(256)  NOT NULL COMMENT '密码哈希 BCrypt',
  contact_phone     VARCHAR(20)   NOT NULL COMMENT '联系电话(预留短信2FA)',
  contact_email     VARCHAR(100)  NOT NULL COMMENT '邮箱',
  invite_code       VARCHAR(32)   NOT NULL COMMENT '邀请码 一次有效',
  invite_used       TINYINT(1)    NOT NULL DEFAULT 0 COMMENT '是否使用',
  invite_expire_time DATETIME     NOT NULL COMMENT '邀请码有效期 默认7天',
  access_expire_time DATETIME     NOT NULL COMMENT '账号有效期 默认1年',
  ip_whitelist      JSON          NULL COMMENT 'IP白名单',
  status            ENUM('正常','锁定','禁用','过期') NOT NULL COMMENT '账号状态',
  fail_count        INT           NOT NULL DEFAULT 0 COMMENT '连续失败次数 >=5锁15分',
  lock_until        DATETIME      NULL COMMENT '锁定截止',
  two_factor_enabled TINYINT(1)   NOT NULL DEFAULT 1 COMMENT '双因素(本期站内验证码)',
  session_timeout_min INT         NOT NULL DEFAULT 30 COMMENT '会话超时(分钟)',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (account_id),
  UNIQUE KEY uk_login (login_name),
  UNIQUE KEY uk_invite (invite_code),
  KEY idx_customer_status (customer_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='门户账号表';

CREATE TABLE t_portal_permission (
  perm_id           VARCHAR(20)   NOT NULL COMMENT '权限ID PK',
  account_id        VARCHAR(20)   NOT NULL COMMENT '账号ID 1:1',
  project_ids       JSON          NOT NULL COMMENT '可见项目数组',
  view_progress     TINYINT(1)    NOT NULL DEFAULT 1 COMMENT '查看项目进度',
  apply_report      TINYINT(1)    NOT NULL DEFAULT 1 COMMENT '申请报告',
  submit_feedback   TINYINT(1)    NOT NULL DEFAULT 1 COMMENT '提交反馈',
  fill_filing       TINYINT(1)    NOT NULL DEFAULT 1 COMMENT '填写备案',
  data_scope        ENUM('全部授权项目','指定项目') NOT NULL COMMENT '数据范围',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (perm_id),
  UNIQUE KEY uk_account (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='门户权限配置表(后台静态)';

CREATE TABLE t_portal_login_log (
  log_id            VARCHAR(20)   NOT NULL COMMENT '日志ID PK',
  account_id        VARCHAR(20)   NOT NULL COMMENT '账号ID',
  login_time        DATETIME      NOT NULL COMMENT '登录时间',
  ip                VARCHAR(50)   NOT NULL COMMENT '登录IP',
  user_agent        VARCHAR(500)  NULL COMMENT '浏览器UA',
  login_status      ENUM('成功','失败','锁定') NOT NULL COMMENT '登录状态',
  fail_reason       VARCHAR(200)  NULL COMMENT '失败原因',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (log_id),
  KEY idx_account (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='门户登录日志表(永久留存)';

CREATE TABLE t_project_progress_snapshot (
  snapshot_id       VARCHAR(20)   NOT NULL COMMENT '快照ID PK',
  project_id        VARCHAR(20)   NOT NULL COMMENT '下游项目ID',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '客户ID',
  project_name      VARCHAR(300)  NOT NULL COMMENT '项目名称',
  progress_pct      INT           NOT NULL COMMENT '进度百分比 0~100',
  current_stage     VARCHAR(100)  NOT NULL COMMENT '当前阶段',
  expected_end_date DATE          NOT NULL COMMENT '预计完成时间',
  delayed_flag      TINYINT(1)    NOT NULL COMMENT '是否延期',
  sync_time         DATETIME      NOT NULL COMMENT '同步时间',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (snapshot_id),
  KEY idx_customer_sync (customer_id, sync_time)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='项目进度快照表(只读下游)';

CREATE TABLE t_report_request (
  request_id        VARCHAR(20)   NOT NULL COMMENT '申请ID PK',
  account_id        VARCHAR(20)   NOT NULL COMMENT '申请人账号',
  project_id        VARCHAR(20)   NOT NULL COMMENT '关联项目',
  report_type       ENUM('测评报告','整改建议','合规证明') NOT NULL COMMENT '报告类型',
  request_reason    TEXT          NOT NULL COMMENT '申请原因',
  receive_email     VARCHAR(100)  NOT NULL COMMENT '接收邮箱',
  request_status    ENUM('待审批','已通过','已驳回','已发放','已过期') NOT NULL COMMENT '申请状态',
  approver_user     VARCHAR(50)   NOT NULL COMMENT '审批人(项目经理下游)',
  approve_time      DATETIME      NULL COMMENT '审批时间',
  report_file_id    VARCHAR(20)   NULL COMMENT '报告文件ID(逻辑关联,无FK避免环)',
  encrypt_method    ENUM('AES-256','SM4') NOT NULL DEFAULT 'AES-256' COMMENT '加密方式',
  link_expire_time  DATETIME      NOT NULL COMMENT '链接有效期 默认72h',
  download_password VARCHAR(20)   NOT NULL COMMENT '解压密码(单独渠道发送)',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (request_id),
  KEY idx_account_status (account_id, request_status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='报告申请表';

CREATE TABLE t_report_file (
  file_id           VARCHAR(20)   NOT NULL COMMENT '文件ID PK',
  request_id        VARCHAR(20)   NOT NULL COMMENT '申请ID 1:1',
  file_name         VARCHAR(200)  NOT NULL COMMENT '文件名',
  file_size         BIGINT        NOT NULL COMMENT '文件大小',
  file_hash         VARCHAR(64)   NOT NULL COMMENT 'SHA-256',
  encrypted_path    VARCHAR(500)  NOT NULL COMMENT '加密存储路径',
  watermark_text    TEXT          NOT NULL COMMENT '水印内容',
  download_count    INT           NOT NULL DEFAULT 0 COMMENT '下载次数',
  frozen_flag       TINYINT(1)    NOT NULL DEFAULT 0 COMMENT '是否冻结',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (file_id),
  UNIQUE KEY uk_request (request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='报告文件表';

CREATE TABLE t_report_issue_log (
  log_id            VARCHAR(20)   NOT NULL COMMENT '日志ID PK',
  request_id        VARCHAR(20)   NOT NULL COMMENT '申请ID',
  issue_time        DATETIME      NOT NULL COMMENT '发放时间',
  issue_method      ENUM('邮件','门户下载') NOT NULL COMMENT '发放方式',
  receiver          VARCHAR(100)  NOT NULL COMMENT '接收人',
  download_time     DATETIME      NULL COMMENT '下载时间',
  download_ip       VARCHAR(50)   NULL COMMENT '下载IP',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (log_id),
  KEY idx_request (request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='报告发放记录表(永久留存)';

CREATE TABLE t_customer_feedback (
  feedback_id       VARCHAR(20)   NOT NULL COMMENT '反馈ID PK',
  account_id        VARCHAR(20)   NOT NULL COMMENT '反馈人',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '客户ID',
  project_id        VARCHAR(20)   NULL COMMENT '关联项目',
  feedback_type     ENUM('异议','投诉','建议') NOT NULL COMMENT '反馈类型',
  content           LONGTEXT      NOT NULL COMMENT '问题描述(富文本)',
  attachments       JSON          NULL COMMENT '附件数组',
  submit_time       DATETIME      NOT NULL COMMENT '提交时间',
  handler           VARCHAR(50)   NULL COMMENT '处理人',
  handle_status     ENUM('待处理','处理中','已回复','已关闭') NOT NULL COMMENT '处理状态',
  handle_result     TEXT          NULL COMMENT '处理结果',
  reply_content     TEXT          NULL COMMENT '回复内容',
  reply_time        DATETIME      NULL COMMENT '回复时间',
  escalated_flag    TINYINT(1)    NOT NULL DEFAULT 0 COMMENT '是否升级(超24h)',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (feedback_id),
  KEY idx_handle_esc (handle_status, escalated_flag)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='客户反馈表(永久留存)';

CREATE TABLE t_service_evaluation (
  eval_id           VARCHAR(20)   NOT NULL COMMENT '评价ID PK',
  project_id        VARCHAR(20)   NOT NULL COMMENT '项目ID',
  account_id        VARCHAR(20)   NOT NULL COMMENT '评价人',
  prof_score        TINYINT       NOT NULL COMMENT '专业能力 1~5',
  response_score    TINYINT       NOT NULL COMMENT '响应速度 1~5',
  attitude_score    TINYINT       NOT NULL COMMENT '服务态度 1~5',
  report_score      TINYINT       NOT NULL COMMENT '报告质量 1~5',
  avg_score         DECIMAL(3,2)  NOT NULL COMMENT '平均分',
  comment           TEXT          NULL COMMENT '评语',
  anonymous_flag    TINYINT(1)    NOT NULL DEFAULT 1 COMMENT '匿名',
  push_to_management TINYINT(1)   NOT NULL DEFAULT 0 COMMENT '推送管理层(avg<3)',
  eval_time         DATETIME      NOT NULL COMMENT '评价时间',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (eval_id),
  KEY idx_account (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='服务评价表(永久留存)';

CREATE TABLE t_filing_record (
  filing_id         VARCHAR(20)   NOT NULL COMMENT '备案ID PK',
  account_id        VARCHAR(20)   NOT NULL COMMENT '填写人',
  customer_id       VARCHAR(20)   NOT NULL COMMENT '客户ID',
  system_name       VARCHAR(200)  NOT NULL COMMENT '系统名称',
  filing_code       VARCHAR(100)  NOT NULL COMMENT '备案编号',
  security_level    ENUM('一级','二级','三级','四级','五级') NOT NULL COMMENT '安全保护等级',
  level_date        DATE          NOT NULL COMMENT '系统定级时间',
  unit_name         VARCHAR(200)  NOT NULL COMMENT '单位名称',
  credit_code       VARCHAR(18)   NOT NULL COMMENT '统一社会信用代码',
  unit_nature       ENUM('党政机关','事业单位','企业','其他') NOT NULL COMMENT '单位性质',
  industry          ENUM('金融','政府','能源','制造','电信','医疗','教育','交通','互联网','其他') NOT NULL COMMENT '所属行业',
  address           VARCHAR(500)  NOT NULL COMMENT '地址',
  contact_person    VARCHAR(50)   NOT NULL COMMENT '联系人',
  contact_phone     VARCHAR(20)   NOT NULL COMMENT '联系电话',
  security_chief    VARCHAR(100)  NOT NULL COMMENT '安全负责人',
  sys_admin         VARCHAR(100)  NOT NULL COMMENT '系统管理员',
  net_admin         VARCHAR(100)  NOT NULL COMMENT '网络管理员',
  server_count      INT           NOT NULL COMMENT '服务器数量',
  main_apps         TEXT          NOT NULL COMMENT '主要应用',
  network_topology  VARCHAR(500)  NULL COMMENT '网络拓扑(文件)',
  security_devices  TEXT          NOT NULL COMMENT '安全设备清单',
  test_org          VARCHAR(200)  NOT NULL COMMENT '测评机构',
  test_time         DATE          NOT NULL COMMENT '测评时间',
  test_conclusion   ENUM('优','良','中','差') NOT NULL COMMENT '测评结论',
  filing_status     ENUM('暂存','已提交','管理员解锁') NOT NULL COMMENT '备案状态',
  submit_time       DATETIME      NULL COMMENT '提交时间',
  generated_pdf     VARCHAR(500)  NULL COMMENT '生成备案PDF',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (filing_id),
  KEY idx_account (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='备案申请表';

-- ===================== 7. 通用附表 =====================

CREATE TABLE t_attachment (
  attachment_id     VARCHAR(20)   NOT NULL COMMENT '附件ID PK',
  biz_type          ENUM('OPPORTUNITY','QUOTATION','BID','FILING','REPORT') NOT NULL COMMENT '业务类型',
  biz_id            VARCHAR(20)   NOT NULL COMMENT '业务ID(多态)',
  file_name         VARCHAR(200)  NOT NULL COMMENT '文件名',
  file_size         BIGINT        NOT NULL COMMENT '大小',
  storage_path      VARCHAR(500)  NOT NULL COMMENT '存储路径',
  upload_user       VARCHAR(50)   NOT NULL COMMENT '上传人',
  upload_time       DATETIME      NOT NULL COMMENT '上传时间',
  md5               VARCHAR(32)   NOT NULL COMMENT 'MD5去重',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (attachment_id),
  KEY idx_biz (biz_type, biz_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='附件表(多态关联,无外键)';

CREATE TABLE t_approval_instance (
  approval_flow_id  VARCHAR(20)   NOT NULL COMMENT '流程实例ID PK',
  biz_type          ENUM('商机阶段','信用调整','报价','作废') NOT NULL COMMENT '业务类型',
  biz_id            VARCHAR(20)   NOT NULL COMMENT '业务ID',
  flow_def_id       VARCHAR(20)   NOT NULL COMMENT '流程定义ID',
  current_node      INT           NOT NULL COMMENT '当前节点',
  status            ENUM('进行中','通过','驳回','撤回') NOT NULL COMMENT '流程状态',
  created_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_time      DATETIME      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (approval_flow_id),
  KEY idx_biz (biz_type, biz_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='通用审批引擎对接表';

-- ===================== 外键约束 (级联策略见 ER 图 V1.0 §3) =====================

ALTER TABLE t_customer_biz            ADD CONSTRAINT fk_biz_customer   FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_customer_org            ADD CONSTRAINT fk_org_customer   FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_customer_stakeholder    ADD CONSTRAINT fk_sh_customer    FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_customer_system         ADD CONSTRAINT fk_sys_customer   FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_customer_finance        ADD CONSTRAINT fk_fin_customer   FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_customer_log            ADD CONSTRAINT fk_log_customer   FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_customer_communication  ADD CONSTRAINT fk_comm_customer  FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_customer                ADD CONSTRAINT fk_cust_merge     FOREIGN KEY (merged_to)     REFERENCES t_customer (customer_id)        ON DELETE SET NULL;
ALTER TABLE t_business_opportunity    ADD CONSTRAINT fk_opp_customer   FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_opportunity_follow      ADD CONSTRAINT fk_follow_opp     FOREIGN KEY (opportunity_id) REFERENCES t_business_opportunity (opportunity_id) ON DELETE CASCADE;
ALTER TABLE t_opportunity_approval    ADD CONSTRAINT fk_appr_opp       FOREIGN KEY (opportunity_id) REFERENCES t_business_opportunity (opportunity_id) ON DELETE CASCADE;
ALTER TABLE t_quotation               ADD CONSTRAINT fk_quo_opp        FOREIGN KEY (opportunity_id) REFERENCES t_business_opportunity (opportunity_id) ON DELETE RESTRICT;
ALTER TABLE t_quotation               ADD CONSTRAINT fk_quo_customer   FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_quotation               ADD CONSTRAINT fk_quo_approval   FOREIGN KEY (approval_flow_id) REFERENCES t_approval_instance (approval_flow_id) ON DELETE RESTRICT;
ALTER TABLE t_quotation_item          ADD CONSTRAINT fk_item_quo       FOREIGN KEY (quotation_id)  REFERENCES t_quotation (quotation_id)        ON DELETE CASCADE;
ALTER TABLE t_quotation_payment_term  ADD CONSTRAINT fk_term_quo       FOREIGN KEY (quotation_id)  REFERENCES t_quotation (quotation_id)        ON DELETE CASCADE;
ALTER TABLE t_quotation_approval      ADD CONSTRAINT fk_qappr_quo      FOREIGN KEY (quotation_id)  REFERENCES t_quotation (quotation_id)        ON DELETE CASCADE;
ALTER TABLE t_bid_project             ADD CONSTRAINT fk_bid_opp        FOREIGN KEY (opportunity_id) REFERENCES t_business_opportunity (opportunity_id) ON DELETE RESTRICT;
ALTER TABLE t_bid_document            ADD CONSTRAINT fk_doc_bid        FOREIGN KEY (bid_id)        REFERENCES t_bid_project (bid_id)          ON DELETE CASCADE;
ALTER TABLE t_bid_deposit             ADD CONSTRAINT fk_dep_bid        FOREIGN KEY (bid_id)        REFERENCES t_bid_project (bid_id)          ON DELETE CASCADE;
ALTER TABLE t_portal_account          ADD CONSTRAINT fk_pa_customer    FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_portal_permission       ADD CONSTRAINT fk_perm_account   FOREIGN KEY (account_id)    REFERENCES t_portal_account (account_id)  ON DELETE CASCADE;
ALTER TABLE t_portal_login_log        ADD CONSTRAINT fk_ll_account     FOREIGN KEY (account_id)    REFERENCES t_portal_account (account_id)  ON DELETE CASCADE;
ALTER TABLE t_project_progress_snapshot ADD CONSTRAINT fk_snap_customer FOREIGN KEY (customer_id)  REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_report_request          ADD CONSTRAINT fk_rr_account     FOREIGN KEY (account_id)    REFERENCES t_portal_account (account_id)  ON DELETE RESTRICT;
ALTER TABLE t_report_file             ADD CONSTRAINT fk_rf_request     FOREIGN KEY (request_id)    REFERENCES t_report_request (request_id) ON DELETE CASCADE;
ALTER TABLE t_report_issue_log        ADD CONSTRAINT fk_il_request     FOREIGN KEY (request_id)    REFERENCES t_report_request (request_id) ON DELETE CASCADE;
ALTER TABLE t_customer_feedback       ADD CONSTRAINT fk_fb_account     FOREIGN KEY (account_id)    REFERENCES t_portal_account (account_id)  ON DELETE RESTRICT;
ALTER TABLE t_customer_feedback       ADD CONSTRAINT fk_fb_customer    FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;
ALTER TABLE t_service_evaluation      ADD CONSTRAINT fk_ev_account     FOREIGN KEY (account_id)    REFERENCES t_portal_account (account_id)  ON DELETE RESTRICT;
ALTER TABLE t_filing_record           ADD CONSTRAINT fk_fl_account     FOREIGN KEY (account_id)    REFERENCES t_portal_account (account_id)  ON DELETE RESTRICT;
ALTER TABLE t_filing_record           ADD CONSTRAINT fk_fl_customer    FOREIGN KEY (customer_id)   REFERENCES t_customer (customer_id)        ON DELETE RESTRICT;

SET FOREIGN_KEY_CHECKS = 1;

-- =============================================================
-- 收尾说明:
--   共 30 张表 (数据字典实际定义 30 张；ER 图 §0 标注"23 张"为旧口径，以字典为准)。
--   建议执行顺序: 先 source 本文件建表，再由 GORM AutoMigrate 或 Flyway/Liquibase 管理后续变更。
--   敏感字段加密、软删除查询过滤、信用评分定时任务由应用层实现(见 V1.3 / T1)。
-- =============================================================

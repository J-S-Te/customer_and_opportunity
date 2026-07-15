/**
 * Mock 数据中心
 * 模拟 23 张表的完整业务数据 + REST 接口
 * 数据结构与《数据字典 V1.0》《ER 图 V1.0》完全对齐
 * 当 VITE_USE_MOCK=false 时，所有 axios 请求走真实后端
 */

import dayjs from 'dayjs'

// ================ 工具函数 ================

let _seq = 1
const nextSeq = (prefix) => `${prefix}${dayjs().format('YYYYMMDD')}${String(_seq++).padStart(4, '0')}`

function delay(ms = 200) {
  return new Promise((r) => setTimeout(r, ms))
}

function ok(data = {}) {
  return { code: 0, message: 'success', data, request_id: 'mock-' + Date.now() }
}

function pageWrap(list, page = 1, pageSize = 20) {
  const total = list.length
  const start = (page - 1) * pageSize
  return {
    list: list.slice(start, start + pageSize),
    pagination: {
      page,
      page_size: pageSize,
      total,
      total_pages: Math.max(1, Math.ceil(total / pageSize))
    }
  }
}

// ================ 枚举字典 ================

export const ENUMS = {
  customer_type: [
    { value: '业主', label: '业主', tag: 'info' },
    { value: '三方', label: '三方', tag: 'warning' },
    { value: '其他', label: '其他', tag: 'gray' },
    { value: '政府', label: '政府', tag: 'success' },
    { value: '事业单位', label: '事业单位', tag: 'purple' },
    { value: '个人', label: '个人', tag: 'gray' }
  ],
  industry: ['金融', '政府', '能源', '制造', '电信', '医疗', '教育', '交通', '互联网', '其他'],
  enterprise_scale: ['大型', '中型', '小型', '微型'],
  customer_status: [
    { value: '潜在', label: '潜在', tag: 'gray' },
    { value: '在跟', label: '在跟', tag: 'info' },
    { value: '成交', label: '成交', tag: 'success' },
    { value: '流失', label: '流失', tag: 'danger' },
    { value: '作废', label: '作废', tag: 'danger' }
  ],
  credit_level: [
    { value: 'A', label: 'A', tag: 'success' },
    { value: 'B', label: 'B', tag: 'info' },
    { value: 'C', label: 'C', tag: 'warning' },
    { value: 'D', label: 'D', tag: 'danger' }
  ],
  opp_source: ['老客户推荐', '公开招标', '自主开发', '客户介绍', '行业活动', '其他'],
  opp_type: ['等保测评', '风险评估', '渗透测试', '代码审计', '安全咨询', '综合'],
  opp_stage: ['初步接触', '需求沟通', '方案制定', '报价', '投标'],
  opp_status: [
    { value: '跟进中', label: '跟进中', tag: 'info' },
    { value: '已签单', label: '已签单', tag: 'success' },
    { value: '已流失', label: '已流失', tag: 'danger' },
    { value: '已作废', label: '已作废', tag: 'gray' }
  ],
  lost_reason: ['价格', '技术', '关系', '客户预算', '竞争对手', '其他'],
  lost_type: ['暂时流失', '永久流失'],
  follow_type: ['上门拜访', '电话', '邮件', '会议', '站内消息'],
  quotation_status: [
    { value: '草稿', label: '草稿', tag: 'gray' },
    { value: '审批中', label: '审批中', tag: 'info' },
    { value: '已生效', label: '已生效', tag: 'success' },
    { value: '已失效', label: '已失效', tag: 'gray' },
    { value: '已转合同', label: '已转合同', tag: 'purple' }
  ],
  bid_status: [
    { value: '准备中', label: '准备中', tag: 'gray' },
    { value: '标书制作', label: '标书制作', tag: 'info' },
    { value: '已投标', label: '已投标', tag: 'warning' },
    { value: '中标', label: '中标', tag: 'success' },
    { value: '落标', label: '落标', tag: 'danger' }
  ],
  deposit_status: ['未退', '已退', '部分退', '待退还', '投标中'],
  system_level: ['一级', '二级', '三级', '四级', '五级'],
  deploy_mode: ['公有云', '私有云', '混合云', '本地', '其他'],
  feedback_type: [
    { value: '异议', label: '异议', tag: 'warning' },
    { value: '投诉', label: '投诉', tag: 'danger' },
    { value: '建议', label: '建议', tag: 'info' }
  ],
  handle_status: [
    { value: '待处理', label: '待处理', tag: 'gray' },
    { value: '处理中', label: '处理中', tag: 'info' },
    { value: '已回复', label: '已回复', tag: 'success' },
    { value: '已关闭', label: '已关闭', tag: 'gray' }
  ],
  report_type: ['测评报告', '整改建议', '合规证明'],
  request_status: [
    { value: '待审批', label: '待审批', tag: 'gray' },
    { value: '已通过', label: '已通过', tag: 'success' },
    { value: '已驳回', label: '已驳回', tag: 'danger' },
    { value: '已发放', label: '已发放', tag: 'purple' },
    { value: '已过期', label: '已过期', tag: 'gray' }
  ],
  unit_nature: ['党政机关', '事业单位', '企业', '其他']
}

// ================ Mock 数据初始化 ================

const db = {
  customers: [
    {
      customer_id: 'KH202606150001', customer_name: '华兴证券股份有限公司', customer_type: '业主',
      credit_code: '91440300100008888K', industry: '金融', enterprise_scale: '大型',
      reg_address: '广东省深圳市福田区福华三路88号', office_address: '深圳市福田区福华三路88号财富大厦',
      customer_status: '成交', credit_level: 'A', credit_score: 92, risk_flag: false,
      is_merged: false, merged_to: null, end_date: null,
      main_business: '证券经纪、投资银行、资产管理、研究咨询',
      test_demand_type: ['等保测评', '渗透测试', '代码审计'],
      last_follow_date: '2026-06-14',
      created_time: '2025-03-12 09:30:00'
    },
    {
      customer_id: 'KH202606120042', customer_name: '鹏程能源科技有限公司', customer_type: '三方',
      credit_code: '91440100000012345X', industry: '能源', enterprise_scale: '中型',
      reg_address: '广东省广州市天河区珠江新城', office_address: '广州市天河区珠江新城A座22楼',
      customer_status: '在跟', credit_level: 'B', credit_score: 85, risk_flag: false,
      is_merged: false, merged_to: null, end_date: null,
      main_business: '新能源技术开发、电力系统集成',
      test_demand_type: ['风险评估', '渗透测试'],
      last_follow_date: '2026-06-13',
      created_time: '2025-05-20 14:20:00'
    },
    {
      customer_id: 'KH202606080157', customer_name: '滨江医疗集团有限公司', customer_type: '政府',
      credit_code: '91330000000067890Y', industry: '医疗', enterprise_scale: '大型',
      reg_address: '浙江省杭州市西湖区文三路', office_address: '杭州市西湖区文三路199号',
      customer_status: '潜在', credit_level: 'C', credit_score: 68, risk_flag: false,
      is_merged: false, merged_to: null, end_date: null,
      main_business: '医疗服务、健康管理',
      test_demand_type: ['等保测评'],
      last_follow_date: '2026-06-10',
      created_time: '2026-04-08 11:00:00'
    },
    {
      customer_id: 'KH202606030289', customer_name: '众诚制造有限责任公司', customer_type: '事业单位',
      credit_code: '91441900000098765Z', industry: '制造', enterprise_scale: '中型',
      reg_address: '广东省东莞市松山湖高新区', office_address: '东莞市松山湖高新区科技二路',
      customer_status: '流失', credit_level: 'D', credit_score: 55, risk_flag: true,
      is_merged: false, merged_to: null, end_date: '2026-05-30',
      main_business: '智能制造装备研发、生产',
      test_demand_type: ['代码审计'],
      last_follow_date: '2026-05-28',
      created_time: '2025-09-15 16:30:00'
    },
    {
      customer_id: 'KH202606090012', customer_name: '星河互联网科技有限公司', customer_type: '业主',
      credit_code: '91110000000055555A', industry: '互联网', enterprise_scale: '大型',
      reg_address: '北京市海淀区中关村', office_address: '北京市海淀区中关村大街1号',
      customer_status: '在跟', credit_level: 'A', credit_score: 95, risk_flag: false,
      is_merged: false, merged_to: null, end_date: null,
      main_business: '云计算、大数据服务',
      test_demand_type: ['等保测评', '渗透测试'],
      last_follow_date: '2026-06-15',
      created_time: '2025-11-08 10:15:00'
    }
  ],

  stakeholders: [
    { stakeholder_id: 'STK0001', customer_id: 'KH202606150001', name: '陈志远', position: '信息技术部总监',
      phone: '138****6789', email: 'chenzy@huaxing.com', decision_weight: 5,
      preference_tags: ['技术型'], last_communicate_time: '2026-06-14' },
    { stakeholder_id: 'STK0002', customer_id: 'KH202606150001', name: '孙晓雯', position: '安全合规主管',
      phone: '139****5678', email: 'sunxw@huaxing.com', decision_weight: 3,
      preference_tags: ['务实型'], last_communicate_time: '2026-06-12' },
    { stakeholder_id: 'STK0003', customer_id: 'KH202606120042', name: '林海峰', position: 'CTO',
      phone: '136****1234', email: 'linhf@pengcheng.com', decision_weight: 5,
      preference_tags: ['价格敏感型'], last_communicate_time: '2026-06-13' }
  ],

  opportunities: [
    { opportunity_id: 'SJ202606100005', customer_id: 'KH202606150001',
      customer_name: '华兴证券股份有限公司',
      opp_name: '核心交易系统等保测评', opp_source: '老客户推荐', opp_type: '等保测评',
      expected_amount: 485000, expected_sign_date: '2026-07-15',
      current_stage: '初步接触', opp_status: '跟进中',
      sales_owner: '张明远', support_team: ['李婷芳', '王建国'],
      competitor_info: [], created_time: '2026-06-10' },
    { opportunity_id: 'SJ202606080012', customer_id: 'KH202606120042',
      customer_name: '鹏程能源科技有限公司',
      opp_name: '风险评估与渗透测试', opp_source: '公开招标', opp_type: '风险评估',
      expected_amount: 1268000, expected_sign_date: '2026-08-20',
      current_stage: '需求沟通', opp_status: '跟进中',
      sales_owner: '李婷芳', support_team: ['王建国'],
      competitor_info: [
        { name: '中诚安全测评有限公司', advantage: '价格优势', disadvantage: '服务覆盖范围较窄',
          our_strategy: '全生命周期服务方案为主打' },
        { name: '安恒信息技术有限公司', advantage: '品牌知名度高', disadvantage: '报价偏高',
          our_strategy: '性价比+快速响应策略切入' }
      ], created_time: '2026-06-08' },
    { opportunity_id: 'SJ202606050008', customer_id: 'KH202606080157',
      customer_name: '滨江医疗集团有限公司',
      opp_name: '医院信息平台安全建设', opp_source: '自主开发', opp_type: '综合',
      expected_amount: 852000, expected_sign_date: '2026-09-10',
      current_stage: '方案制定', opp_status: '跟进中',
      sales_owner: '王建国', support_team: ['陈浩然'],
      competitor_info: [], created_time: '2026-06-05' },
    { opportunity_id: 'SJ202606030003', customer_id: 'KH202606030289',
      customer_name: '众诚制造有限责任公司',
      opp_name: '智能制造代码审计', opp_source: '客户介绍', opp_type: '代码审计',
      expected_amount: 326000, expected_sign_date: '2026-07-30',
      current_stage: '方案制定', opp_status: '跟进中',
      sales_owner: '赵晓燕', support_team: [],
      competitor_info: [], created_time: '2026-06-03' },
    { opportunity_id: 'SJ202606140006', customer_id: 'KH202606150001',
      customer_name: '华兴证券股份有限公司',
      opp_name: '金融云安全合规服务', opp_source: '老客户推荐', opp_type: '等保测评',
      expected_amount: 485000, expected_sign_date: '2026-08-05',
      current_stage: '报价', opp_status: '跟进中',
      sales_owner: '陈浩然', support_team: ['张明远'],
      competitor_info: [], created_time: '2026-06-14' },
    { opportunity_id: 'SJ202606090015', customer_id: 'KH202606120042',
      customer_name: '鹏程能源科技有限公司',
      opp_name: '政府云等保三级测评', opp_source: '公开招标', opp_type: '等保测评',
      expected_amount: 2103000, expected_sign_date: '2026-09-25',
      current_stage: '投标', opp_status: '跟进中',
      sales_owner: '刘思涵', support_team: ['王建国'],
      competitor_info: [], created_time: '2026-06-09' }
  ],

  follows: [
    { follow_id: 'FL001', opportunity_id: 'SJ202606080012', follow_time: '2026-06-13 14:00:00',
      follow_type: '电话沟通', stage_before: '初步接触', stage_after: '需求沟通',
      content: '客户技术部门已确认安全测试范围，提出需要代码审计 + 渗透测试组合服务。对方案质量和响应速度要求较高。',
      customer_feedback: '希望 2 周内能看到详细方案', next_follow_time: '2026-06-20 10:00:00',
      key_result: '完成需求范围确认' },
    { follow_id: 'FL002', opportunity_id: 'SJ202606080012', follow_time: '2026-06-10 09:30:00',
      follow_type: '上门拜访', stage_before: '初步接触', stage_after: '初步接触',
      content: '拜访客户信息技术部，了解系统架构与现有安全措施。客户对测评报告格式有特殊要求。',
      customer_feedback: '需要包含等保 2.0 标准', next_follow_time: '2026-06-13 14:00:00',
      key_result: '建立初次沟通' },
    { follow_id: 'FL003', opportunity_id: 'SJ202606080012', follow_time: '2026-06-08 16:00:00',
      follow_type: '电话', stage_before: '初步接触', stage_after: '初步接触',
      content: '通过公开招标信息发现项目机会，初步联系客户采购部门。客户表示欢迎供应商参与方案交流。',
      customer_feedback: '愿意进一步沟通', next_follow_time: '2026-06-10 09:30:00',
      key_result: '建立联系' }
  ],

  quotations: [
    { quotation_id: 'BJ202606150001', opportunity_id: 'SJ202606140006',
      customer_id: 'KH202606150001', customer_name: '华兴证券股份有限公司',
      opp_name: '金融云安全合规服务',
      quotation_date: '2026-06-14', effective_end_date: '2026-07-14',
      subtotal: 480000, discount_amount: 20000, tax_amount: 27600, total_amount: 487600,
      payment_terms_default: true, service_period: '自合同签订之日起 90 个工作日',
      warranty_terms: '质保期 1 年', special_terms: '',
      quotation_status: '已生效', version: 1, below_price_flag: false,
      created_by: '陈浩然', created_time: '2026-06-14 10:00:00' },
    { quotation_id: 'BJ202606120035', opportunity_id: 'SJ202606080012',
      customer_id: 'KH202606120042', customer_name: '鹏程能源科技有限公司',
      opp_name: '风险评估与渗透测试',
      quotation_date: '2026-06-12', effective_end_date: '2026-07-12',
      subtotal: 1200000, discount_amount: 0, tax_amount: 72000, total_amount: 1272000,
      payment_terms_default: true, service_period: '120 个工作日',
      warranty_terms: '质保期 6 个月', special_terms: '客户提供测试环境',
      quotation_status: '审批中', version: 1, below_price_flag: false,
      created_by: '李婷芳', created_time: '2026-06-12 11:30:00' },
    { quotation_id: 'BJ202605280012', opportunity_id: 'SJ202606030003',
      customer_id: 'KH202606030289', customer_name: '众诚制造有限责任公司',
      opp_name: '智能制造代码审计',
      quotation_date: '2026-05-28', effective_end_date: '2026-06-27',
      subtotal: 320000, discount_amount: 0, tax_amount: 19200, total_amount: 339200,
      payment_terms_default: true, service_period: '60 个工作日',
      warranty_terms: '', special_terms: '',
      quotation_status: '已失效', version: 1, below_price_flag: false,
      created_by: '赵晓燕', created_time: '2026-05-28 09:00:00' }
  ],

  quotation_items: [
    { item_id: 'QI001', quotation_id: 'BJ202606150001', service_item: '等级保护测评',
      service_content: '三级等保测评（含整改咨询）', quantity: 1, unit_price: 400000,
      discount_rate: 0.95, amount: 380000, remark: '' },
    { item_id: 'QI002', quotation_id: 'BJ202606150001', service_item: '渗透测试服务',
      service_content: '内外网渗透测试 + 报告', quantity: 1, unit_price: 100000,
      discount_rate: 1.0, amount: 100000, remark: '' }
  ],

  payment_terms: [
    { term_id: 'PT001', quotation_id: 'BJ202606150001',
      phase_name: '合同签定后 10 工作日', pay_ratio: 0.5,
      condition_desc: '合同签订后 10 个工作日内', sort_order: 1 },
    { term_id: 'PT002', quotation_id: 'BJ202606150001',
      phase_name: '验收后', pay_ratio: 0.5,
      condition_desc: '项目验收合格后', sort_order: 2 }
  ],

  quotation_approvals: [
    { approval_id: 'QA001', quotation_id: 'BJ202606150001', node_order: 1,
      approver_user: '刘志强', approve_status: '通过',
      approve_time: '2026-06-14 14:30:00', approve_opinion: '同意报价方案，建议加强售后条款说明' },
    { approval_id: 'QA002', quotation_id: 'BJ202606150001', node_order: 2,
      approver_user: '张慧敏', approve_status: '通过',
      approve_time: '2026-06-14 16:45:00', approve_opinion: '金额在预算范围内，付款条款按标准约定执行' },
    { approval_id: 'QA003', quotation_id: 'BJ202606120035', node_order: 1,
      approver_user: '刘志强', approve_status: '待审批',
      approve_time: null, approve_opinion: '' }
  ],

  bids: [
    { bid_id: 'TB202606100001', opportunity_id: 'SJ202606140006',
      project_name: '金融云安全合规服务', bid_code: 'ZB2026-GD-SZ-0856',
      tender_name: '华兴证券股份有限公司', agency_name: '深圳市采购中心',
      bid_deadline: '2026-07-15 17:00:00', bid_status: '标书制作',
      bid_result_time: null, bid_rank: null,
      winning_notice: null, lost_reason: null,
      deposit: { amount: 100000, pay_time: '2026-06-11', expected_refund_date: '2026-09-15',
        refund_status: '待退还', refund_amount: null, refund_time: null },
      created_time: '2026-06-10' },
    { bid_id: 'TB202606080003', opportunity_id: 'SJ202606090015',
      project_name: '政府云等保三级测评', bid_code: 'ZB2026-GD-GZ-1203',
      tender_name: '鹏程能源科技有限公司', agency_name: '广州市公共资源交易中心',
      bid_deadline: '2026-08-05 17:00:00', bid_status: '已投标',
      bid_result_time: '2026-08-10 10:00:00', bid_rank: null,
      winning_notice: null, lost_reason: null,
      deposit: { amount: 200000, pay_time: '2026-06-09', expected_refund_date: '2026-12-05',
        refund_status: '投标中', refund_amount: null, refund_time: null },
      created_time: '2026-06-08' },
    { bid_id: 'TB202605200005', opportunity_id: 'SJ202606100005',
      project_name: '核心交易系统等保测评项目', bid_code: 'ZB2026-GD-SZ-0782',
      tender_name: '华兴证券股份有限公司', agency_name: '深圳市采购中心',
      bid_deadline: '2026-06-20 17:00:00', bid_status: '中标',
      bid_result_time: '2026-06-25 10:00:00', bid_rank: 1,
      winning_notice: '/files/notice-001.pdf', lost_reason: null,
      deposit: { amount: 80000, pay_time: '2026-05-22', expected_refund_date: '2026-07-20',
        refund_status: '待退还', refund_amount: null, refund_time: null },
      created_time: '2026-05-20' }
  ],

  portal_projects: [
    { project_id: 'XM202606090001', project_name: '核心交易系统等保测评',
      contract_id: 'HT202606090001', customer_id: 'KH202606150001',
      project_status: '方案制定中', progress_pct: 65,
      current_stage: '方案制定', expected_end_date: '2026-09-15',
      delayed_flag: false, sync_time: '2026-06-15 10:30:00' },
    { project_id: 'XM202605150003', project_name: '金融云安全合规服务',
      contract_id: 'HT202605150003', customer_id: 'KH202606150001',
      project_status: '已完成', progress_pct: 100,
      current_stage: '已交付', expected_end_date: '2026-06-10',
      delayed_flag: false, sync_time: '2026-06-15 10:30:00' },
    { project_id: 'XM202604200007', project_name: '风险评估与渗透测试',
      contract_id: 'HT202604200007', customer_id: 'KH202606150001',
      project_status: '进行中', progress_pct: 35,
      current_stage: '测试执行', expected_end_date: '2026-07-30',
      delayed_flag: false, sync_time: '2026-06-15 10:30:00' }
  ],

  report_requests: [
    { request_id: 'RR001', account_id: 'PA001', project_id: 'XM202605150003',
      report_type: '测评报告', request_reason: '内部合规审查',
      receive_email: 'chenzy@huaxing.com',
      request_status: '已发放', approver_user: '王建国',
      approve_time: '2026-06-14 14:00:00',
      encrypt_method: 'AES-256', link_expire_time: '2026-06-17 23:59:59',
      file_name: '金融云安全合规测评报告.pdf', file_size: 2456789, download_count: 1,
      submit_time: '2026-06-14 10:00:00' },
    { request_id: 'RR002', account_id: 'PA001', project_id: 'XM202606090001',
      report_type: '测评报告', request_reason: '监管部门检查',
      receive_email: 'chenzy@huaxing.com',
      request_status: '待审批', approver_user: '王建国',
      approve_time: null,
      encrypt_method: 'AES-256', link_expire_time: null,
      file_name: null, file_size: null, download_count: 0,
      submit_time: '2026-06-15 09:00:00' }
  ],

  feedback: [
    { feedback_id: 'FB001', account_id: 'PA001', customer_id: 'KH202606150001',
      project_id: 'XM202606090001', feedback_type: '建议',
      content: '希望增加定期进度报告推送功能，每周一发送项目进度邮件',
      submit_time: '2026-06-10 14:00:00', handler: '客户成功部-李婷芳',
      handle_status: '已回复', handle_result: '已采纳建议，排期至 7 月份',
      reply_content: '感谢建议，我们将在 7 月份上线该功能', reply_time: '2026-06-11 10:00:00',
      escalated_flag: false }
  ],

  filings: [
    { filing_id: 'FLG001', account_id: 'PA001', customer_id: 'KH202606150001',
      system_name: '核心交易系统', filing_code: '440306-2026-001',
      security_level: '三级', level_date: '2026-01-15',
      unit_name: '华兴证券股份有限公司', credit_code: '91440300100008888K',
      unit_nature: '企业', industry: '金融', address: '深圳市福田区福华三路88号',
      contact_person: '陈志远', contact_phone: '13812346789',
      security_chief: '陈志远 · 安全总监 · 13812346789',
      sys_admin: '孙晓雯 · 安全主管 · 13912345678',
      net_admin: '王磊 · 网络工程师 · 13712345566',
      server_count: 12, main_apps: '核心交易系统、风险监控系统',
      security_devices: '防火墙 ×2、IDS ×1、WAF ×1、堡垒机 ×1',
      test_org: '安信检测技术有限公司', test_time: '2026-06-15', test_conclusion: '良',
      filing_status: '暂存', submit_time: null, generated_pdf: null,
      created_time: '2026-06-10 10:00:00' }
  ],

  customer_logs: [
    { log_id: 'LOG001', customer_id: 'KH202606150001', op_type: '编辑',
      op_user: '张明远', op_time: '2026-06-14 14:30:00',
      change_content: '主营业务: 证券经纪、投行 → 证券经纪、投资银行、资产管理、研究咨询',
      op_reason: '' },
    { log_id: 'LOG002', customer_id: 'KH202606150001', op_type: '新增',
      op_user: '张明远', op_time: '2025-03-12 09:30:00',
      change_content: '客户档案创建',
      op_reason: '' },
    { log_id: 'LOG003', customer_id: 'KH202606150001', op_type: '信用调整',
      op_user: '系统', op_time: '2026-06-01 02:00:00',
      change_content: '信用等级: B → A（评分 92）',
      op_reason: '连续 6 个月付款及时，自动升级' }
  ],

  customer_systems: [
    { biz_system_id: 'BS001', customer_id: 'KH202606150001', system_name: '核心交易系统',
      system_level: '三级', deploy_mode: '私有云', system_count: 1,
      test_history: '2024 年完成等保三级测评，整改后通过' },
    { biz_system_id: 'BS002', customer_id: 'KH202606150001', system_name: '网上交易系统',
      system_level: '三级', deploy_mode: '混合云', system_count: 2,
      test_history: '2024 年完成测评' }
  ],

  customer_finance: [
    { customer_id: 'KH202606150001', bank_account: '6222****7890',
      account_name: '华兴证券股份有限公司', bank_name: '中国工商银行深圳福田支行',
      invoice_title: '华兴证券股份有限公司', tax_no: '91440300100008888K',
      invoice_address: '深圳市福田区福华三路88号 0755-88888888' }
  ],

  customer_communications: [
    { comm_id: 'CM001', customer_id: 'KH202606150001', stakeholder_id: 'STK0001',
      comm_time: '2026-06-14 10:00:00', comm_type: '上门拜访',
      content: '拜访客户信息技术部总监陈志远，沟通新一年度测评计划',
      feedback: '希望针对核心系统做深度测评', next_step: '下周二发送方案初稿' }
  ]
}

// ================ 路由匹配 ================

function matchPath(pattern, path) {
  // 简单路径参数提取
  const pParts = pattern.split('/').filter(Boolean)
  const tParts = path.split('/').filter(Boolean)
  if (pParts.length !== tParts.length) return null
  const params = {}
  for (let i = 0; i < pParts.length; i++) {
    if (pParts[i].startsWith(':')) {
      params[pParts[i].slice(1)] = decodeURIComponent(tParts[i])
    } else if (pParts[i] !== tParts[i]) {
      return null
    }
  }
  return params
}

// ================ 启动 Mock 拦截 ================

export function startMock(axiosInstance) {
  // 拦截所有 axios 请求
  axiosInstance.interceptors.request.use(async (config) => {
    // 通过 adapter 直接 mock 返回
    config.adapter = async (mockConfig) => {
      await delay(150 + Math.random() * 150)
      return handleRequest(mockConfig)
    }
    return config
  })
}

async function handleRequest(config) {
  let { url = '', method = 'get', params = {}, data = null } = config
  method = method.toLowerCase()

  // 去掉 query string
  const [path, qs] = url.split('?')
  if (qs && !Object.keys(params).length) {
    qs.split('&').forEach((kv) => {
      const [k, v] = kv.split('=')
      if (k) params[k] = decodeURIComponent(v || '')
    })
  }
  if (data && typeof data === 'string') {
    try { data = JSON.parse(data) } catch (e) { /* ignore */ }
  }

  try {
    const result = await routeHandler(path, method, params, data)
    return {
      data: result,
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
      request: {}
    }
  } catch (err) {
    return {
      data: { code: err.code || 500, message: err.message || 'Mock Error', data: null },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
      request: {}
    }
  }
}

async function routeHandler(path, method, params, body) {
  // ====== 认证 ======
  if (path === '/admin/login' && method === 'post') {
    return ok({
      token: 'mock-admin-token-' + Date.now(),
      user: { user_id: 'U001', name: '张明远', role: 'sales_manager', avatar: '' }
    })
  }
  if (path === '/portal/login' && method === 'post') {
    return ok({
      token: 'mock-portal-token-' + Date.now(),
      account: {
        account_id: 'PA001', customer_id: 'KH202606150001',
        customer_name: '华兴证券股份有限公司', contact_phone: '13812346789'
      },
      expires_in: 1800
    })
  }

  // ====== 客户管理 ======
  if (path === '/customers' && method === 'get') {
    let list = [...db.customers]
    if (params.keyword) {
      const kw = params.keyword.toLowerCase()
      list = list.filter(c =>
        c.customer_name.toLowerCase().includes(kw) || c.customer_id.toLowerCase().includes(kw)
      )
    }
    if (params.customer_type) list = list.filter(c => c.customer_type === params.customer_type)
    if (params.industry) list = list.filter(c => c.industry === params.industry)
    if (params.credit_level) list = list.filter(c => c.credit_level === params.credit_level)
    if (params.customer_status) list = list.filter(c => c.customer_status === params.customer_status)
    return ok(pageWrap(list, +params.page || 1, +params.page_size || 20))
  }
  if (path === '/customers' && method === 'post') {
    const id = nextSeq('KH')
    const newCustomer = {
      customer_id: id,
      customer_name: body.customer_name,
      customer_type: body.customer_type,
      credit_code: body.credit_code,
      industry: body.industry,
      enterprise_scale: body.enterprise_scale,
      reg_address: body.reg_address,
      office_address: body.office_address,
      customer_status: '潜在',
      credit_level: 'C',
      credit_score: 70,
      risk_flag: false,
      is_merged: false,
      merged_to: null,
      end_date: null,
      main_business: body.biz_info?.main_business || '',
      test_demand_type: body.biz_info?.test_demand_type || [],
      last_follow_date: dayjs().format('YYYY-MM-DD'),
      created_time: dayjs().format('YYYY-MM-DD HH:mm:ss')
    }
    db.customers.unshift(newCustomer)
    return ok({ customer_id: id })
  }

  // 客户详情
  const customerDetailMatch = matchPath('/customers/:id', path)
  if (customerDetailMatch && method === 'get') {
    const c = db.customers.find(x => x.customer_id === customerDetailMatch.id)
    if (!c) throw new Error('客户不存在')
    return ok({
      basic: c,
      biz_info: { main_business: c.main_business, test_demand_type: c.test_demand_type },
      stakeholders: db.stakeholders.filter(s => s.customer_id === c.customer_id),
      systems: db.customer_systems.filter(s => s.customer_id === c.customer_id),
      finance: (db.customer_finance.find(f => f.customer_id === c.customer_id) || {}),
      opportunities: db.opportunities.filter(o => o.customer_id === c.customer_id),
      recent_logs: db.customer_logs.filter(l => l.customer_id === c.customer_id).slice(0, 10)
    })
  }

  if (customerDetailMatch && method === 'put') {
    const c = db.customers.find(x => x.customer_id === customerDetailMatch.id)
    if (!c) throw new Error('客户不存在')
    Object.assign(c, body)
    return ok({ customer_id: c.customer_id })
  }

  if (path === '/customers/check-duplicate' && method === 'post') {
    const { customer_name, credit_code } = body
    const duplicates = []
    db.customers.forEach(c => {
      if (credit_code && c.credit_code === credit_code) {
        duplicates.push({ customer_id: c.customer_id, customer_name: c.customer_name,
          match_type: 'credit_code_exact', similarity: 1.0 })
      } else if (customer_name && c.customer_name.includes(customer_name.slice(0, 4))) {
        duplicates.push({ customer_id: c.customer_id, customer_name: c.customer_name,
          match_type: 'name_fuzzy', similarity: 0.85 })
      }
    })
    return ok({ has_duplicate: duplicates.length > 0, duplicates })
  }

  if (path === '/customers/stats' && method === 'get') {
    return ok({
      total: db.customers.length,
      following: db.customers.filter(c => c.customer_status === '在跟').length,
      high_value: db.customers.filter(c => c.credit_level === 'A').length,
      to_follow: db.customers.filter(c => c.customer_status === '潜在').length
    })
  }

  // 客户日志
  const customerLogsMatch = path.match(/^\/customers\/([^/]+)\/logs$/)
  if (customerLogsMatch && method === 'get') {
    const logs = db.customer_logs.filter(l => l.customer_id === customerLogsMatch[1])
    return ok(pageWrap(logs, +params.page || 1, +params.page_size || 20))
  }

  // ====== 商机管理 ======
  if (path === '/opportunities' && method === 'get') {
    let list = [...db.opportunities]
    if (params.stage) list = list.filter(o => o.current_stage === params.stage)
    if (params.status) list = list.filter(o => o.opp_status === params.status)
    if (params.customer_id) list = list.filter(o => o.customer_id === params.customer_id)
    if (params.keyword) {
      const kw = params.keyword.toLowerCase()
      list = list.filter(o => o.opp_name.toLowerCase().includes(kw))
    }
    return ok(pageWrap(list, +params.page || 1, +params.page_size || 20))
  }
  if (path === '/opportunities' && method === 'post') {
    const id = nextSeq('SJ')
    const customer = db.customers.find(c => c.customer_id === body.customer_id)
    const newOpp = {
      opportunity_id: id,
      customer_id: body.customer_id,
      customer_name: customer?.customer_name || '',
      opp_name: body.opp_name,
      opp_source: body.opp_source,
      opp_type: body.opp_type,
      expected_amount: body.expected_amount,
      expected_sign_date: body.expected_sign_date,
      current_stage: '初步接触',
      opp_status: '跟进中',
      sales_owner: body.sales_owner,
      support_team: body.support_team || [],
      competitor_info: body.competitor_info || [],
      created_time: dayjs().format('YYYY-MM-DD')
    }
    db.opportunities.unshift(newOpp)
    return ok({ opportunity_id: id })
  }

  const oppDetailMatch = matchPath('/opportunities/:id', path)
  if (oppDetailMatch && method === 'get') {
    const o = db.opportunities.find(x => x.opportunity_id === oppDetailMatch.id)
    if (!o) throw new Error('商机不存在')
    return ok({
      basic: o,
      follows: db.follows.filter(f => f.opportunity_id === o.opportunity_id),
      approvals: [],
      latest_quotation: db.quotations.find(q => q.opportunity_id === o.opportunity_id) || null
    })
  }

  // 跟进记录
  const oppFollowMatch = path.match(/^\/opportunities\/([^/]+)\/follows$/)
  if (oppFollowMatch && method === 'post') {
    const newFollow = {
      follow_id: 'FL' + String(db.follows.length + 1).padStart(3, '0'),
      opportunity_id: oppFollowMatch[1],
      follow_time: dayjs().format('YYYY-MM-DD HH:mm:ss'),
      follow_type: body.follow_type,
      stage_before: body.stage_before || '',
      stage_after: body.stage_after || '',
      content: body.content,
      customer_feedback: body.customer_feedback || '',
      next_follow_time: body.next_follow_time,
      key_result: body.key_result || ''
    }
    db.follows.unshift(newFollow)
    return ok(newFollow)
  }

  // 阶段推进
  const advanceMatch = path.match(/^\/opportunities\/([^/]+)\/advance-stage$/)
  if (advanceMatch && method === 'post') {
    const o = db.opportunities.find(x => x.opportunity_id === advanceMatch[1])
    if (o) o.current_stage = body.to_stage
    return ok({ new_stage: body.to_stage, need_approval: true })
  }

  // 转合同
  const transferMatch = path.match(/^\/opportunities\/([^/]+)\/transfer-to-contract$/)
  if (transferMatch && method === 'post') {
    const o = db.opportunities.find(x => x.opportunity_id === transferMatch[1])
    if (o) {
      o.opp_status = '已签单'
      o.current_stage = '签单'
    }
    return ok({ contract_id: 'HT' + Date.now(), message: '已推送至合同管理子系统' })
  }

  // ====== 报价管理 ======
  if (path === '/quotations' && method === 'get') {
    let list = [...db.quotations]
    if (params.quotation_status) list = list.filter(q => q.quotation_status === params.quotation_status)
    if (params.keyword) {
      const kw = params.keyword.toLowerCase()
      list = list.filter(q =>
        q.quotation_id.toLowerCase().includes(kw) || q.opp_name.toLowerCase().includes(kw)
      )
    }
    return ok(pageWrap(list, +params.page || 1, +params.page_size || 20))
  }
  if (path === '/quotations' && method === 'post') {
    const id = nextSeq('BJ')
    const opp = db.opportunities.find(o => o.opportunity_id === body.opportunity_id)
    const newQuote = {
      quotation_id: id,
      opportunity_id: body.opportunity_id,
      customer_id: opp?.customer_id || '',
      customer_name: opp?.customer_name || '',
      opp_name: opp?.opp_name || '',
      quotation_date: dayjs().format('YYYY-MM-DD'),
      effective_end_date: dayjs().add(30, 'day').format('YYYY-MM-DD'),
      subtotal: 0, discount_amount: 0, tax_amount: 0, total_amount: 0,
      payment_terms_default: body.payment_terms_default !== false,
      service_period: body.service_period || '',
      warranty_terms: body.warranty_terms || '',
      special_terms: body.special_terms || '',
      quotation_status: '草稿',
      version: 1, below_price_flag: false,
      created_by: '当前用户',
      created_time: dayjs().format('YYYY-MM-DD HH:mm:ss')
    }
    // 计算金额
    if (body.items?.length) {
      let subtotal = 0, discount = 0
      body.items.forEach(it => {
        const lineTotal = it.quantity * it.unit_price
        subtotal += lineTotal
        discount += lineTotal * (1 - (it.discount_rate || 1))
      })
      const afterDiscount = subtotal - discount
      const tax = afterDiscount * 0.06
      newQuote.subtotal = subtotal
      newQuote.discount_amount = discount
      newQuote.tax_amount = tax
      newQuote.total_amount = afterDiscount + tax
    }
    db.quotations.unshift(newQuote)
    return ok({ quotation_id: id })
  }

  const quoteDetailMatch = matchPath('/quotations/:id', path)
  if (quoteDetailMatch && method === 'get') {
    const q = db.quotations.find(x => x.quotation_id === quoteDetailMatch.id)
    if (!q) throw new Error('报价单不存在')
    return ok({
      basic: q,
      items: db.quotation_items.filter(i => i.quotation_id === q.quotation_id),
      payment_terms: db.payment_terms.filter(p => p.quotation_id === q.quotation_id),
      approvals: db.quotation_approvals.filter(a => a.quotation_id === q.quotation_id)
    })
  }

  const quoteApprovalMatch = path.match(/^\/quotations\/([^/]+)\/submit-approval$/)
  if (quoteApprovalMatch && method === 'post') {
    const q = db.quotations.find(x => x.quotation_id === quoteApprovalMatch[1])
    if (q) q.quotation_status = '审批中'
    return ok({ message: '已提交审批' })
  }

  // ====== 投标管理 ======
  if (path === '/bids' && method === 'get') {
    let list = [...db.bids]
    if (params.bid_status) list = list.filter(b => b.bid_status === params.bid_status)
    return ok(pageWrap(list, +params.page || 1, +params.page_size || 20))
  }
  if (path === '/bids/stats' && method === 'get') {
    return ok({
      total: db.bids.length,
      active: db.bids.filter(b => ['标书制作', '已投标', '准备中'].includes(b.bid_status)).length,
      win_rate: 0.625,
      pending_deposit: 280000
    })
  }
  if (path === '/bids' && method === 'post') {
    const id = nextSeq('TB')
    const newBid = {
      bid_id: id,
      opportunity_id: body.opportunity_id,
      project_name: body.project_name,
      bid_code: body.bid_code,
      tender_name: body.tender_name,
      agency_name: body.agency_name,
      bid_deadline: body.bid_deadline,
      bid_status: '准备中',
      bid_result_time: null, bid_rank: null,
      winning_notice: null, lost_reason: null,
      deposit: body.deposit,
      created_time: dayjs().format('YYYY-MM-DD')
    }
    db.bids.unshift(newBid)
    return ok({ bid_id: id })
  }

  const bidDetailMatch = matchPath('/bids/:id', path)
  if (bidDetailMatch && method === 'get') {
    const b = db.bids.find(x => x.bid_id === bidDetailMatch.id)
    if (!b) throw new Error('投标项目不存在')
    return ok(b)
  }

  // ====== 门户 ======
  if (path === '/portal/dashboard' && method === 'get') {
    return ok({
      active_projects: 3,
      completed_projects: 12,
      pending_reports: 1,
      pending_evaluations: 2,
      recent_projects: db.portal_projects.slice(0, 3)
    })
  }
  if (path === '/portal/projects' && method === 'get') {
    return ok({ list: db.portal_projects })
  }
  const projProgMatch = matchPath('/portal/projects/:id/progress', path)
  if (projProgMatch && method === 'get') {
    const p = db.portal_projects.find(x => x.project_id === projProgMatch.id)
    return ok(p || {})
  }

  if (path === '/portal/reports/request' && method === 'post') {
    const newReq = {
      request_id: 'RR' + String(db.report_requests.length + 1).padStart(3, '0'),
      account_id: 'PA001',
      project_id: body.project_id,
      report_type: body.report_type,
      request_reason: body.request_reason,
      receive_email: body.receive_email,
      request_status: '待审批',
      approver_user: '王建国',
      approve_time: null,
      encrypt_method: 'AES-256',
      link_expire_time: null,
      file_name: null,
      submit_time: dayjs().format('YYYY-MM-DD HH:mm:ss')
    }
    db.report_requests.unshift(newReq)
    return ok(newReq)
  }
  if (path === '/portal/reports/requests' && method === 'get') {
    return ok({ list: db.report_requests })
  }

  if (path === '/portal/feedbacks' && method === 'post') {
    const newFb = {
      feedback_id: 'FB' + String(db.feedback.length + 1).padStart(3, '0'),
      account_id: 'PA001',
      customer_id: 'KH202606150001',
      project_id: body.project_id,
      feedback_type: body.feedback_type,
      content: body.content,
      submit_time: dayjs().format('YYYY-MM-DD HH:mm:ss'),
      handler: null,
      handle_status: '待处理',
      handle_result: null,
      reply_content: null,
      reply_time: null,
      escalated_flag: false
    }
    db.feedback.unshift(newFb)
    return ok(newFb)
  }

  if (path === '/portal/evaluations' && method === 'post') {
    const avg = ((body.prof_score + body.response_score + body.attitude_score + body.report_score) / 4).toFixed(2)
    return ok({
      eval_id: 'EV' + Date.now(),
      avg_score: parseFloat(avg),
      push_to_management: avg < 3
    })
  }

  if (path === '/portal/filing' && method === 'post') {
    const newFiling = {
      filing_id: 'FLG' + String(db.filings.length + 1).padStart(3, '0'),
      ...body,
      account_id: 'PA001',
      customer_id: 'KH202606150001',
      filing_status: body.filing_status || '暂存',
      generated_pdf: null,
      created_time: dayjs().format('YYYY-MM-DD HH:mm:ss')
    }
    if (newFiling.filing_status === '已提交') {
      newFiling.submit_time = dayjs().format('YYYY-MM-DD HH:mm:ss')
    }
    db.filings.unshift(newFiling)
    return ok(newFiling)
  }

  // ====== 通用 ======
  if (path === '/dict/enums' && method === 'get') {
    return ok(ENUMS)
  }
  if (path === '/auth/me' && method === 'get') {
    return ok({
      user_id: 'U001', name: '张明远', role: 'sales_manager',
      permissions: ['customer:read', 'customer:write', 'opportunity:*', 'quotation:*', 'bid:*']
    })
  }

  // ===== 默认 =====
  throw { code: 404, message: `Mock 接口未实现: ${method.toUpperCase()} ${path}` }
}

export default db

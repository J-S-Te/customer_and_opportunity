/**
 * 系统中所有枚举的元数据
 * 数据字典 V1.0 §1-§7
 */

export const CUSTOMER_TYPES = ['业主', '三方', '其他', '政府', '事业单位', '个人']
export const INDUSTRIES = ['金融', '政府', '能源', '制造', '电信', '医疗', '教育', '交通', '互联网', '其他']
export const ENTERPRISE_SCALES = ['大型', '中型', '小型', '微型']
export const CUSTOMER_STATUS = ['潜在', '在跟', '成交', '流失', '作废']
export const CREDIT_LEVELS = ['A', 'B', 'C', 'D']
export const OPP_SOURCES = ['老客户推荐', '公开招标', '自主开发', '客户介绍', '行业活动', '其他']
export const OPP_TYPES = ['等保测评', '风险评估', '渗透测试', '代码审计', '安全咨询', '综合']
export const OPP_STAGES = ['初步接触', '需求沟通', '方案制定', '报价', '投标']
export const OPP_STATUSES = ['跟进中', '已签单', '已流失', '已作废']
export const LOST_REASONS = ['价格', '技术', '关系', '客户预算', '竞争对手', '其他']
export const LOST_TYPES = ['暂时流失', '永久流失']
export const FOLLOW_TYPES = ['上门拜访', '电话', '邮件', '会议', '站内消息']
export const QUOTATION_STATUSES = ['草稿', '审批中', '已生效', '已失效', '已转合同']
export const BID_STATUSES = ['准备中', '标书制作', '已投标', '中标', '落标']
export const SYSTEM_LEVELS = ['一级', '二级', '三级', '四级', '五级']
export const DEPLOY_MODES = ['公有云', '私有云', '混合云', '本地', '其他']
export const TEST_DEMAND_TYPES = ['等保测评', '风险评估', '渗透测试', '代码审计', '安全咨询', '其他']
export const FEEDBACK_TYPES = ['异议', '投诉', '建议']
export const REPORT_TYPES = ['测评报告', '整改建议', '合规证明']
export const UNIT_NATURES = ['党政机关', '事业单位', '企业', '其他']
export const PREFERENCE_TAGS = ['务实型', '技术型', '关系型', '价格敏感型']
export const DECISION_WEIGHTS = [1, 2, 3, 4, 5]

// 信用评分阈值（数据字典 §6）
export const CREDIT_THRESHOLDS = [
  { min: 90, level: 'A', color: '#059669', bg: '#ECFDF5' },
  { min: 75, level: 'B', color: '#2563EB', bg: '#EFF6FF' },
  { min: 60, level: 'C', color: '#F59E0B', bg: '#FEF3C7' },
  { min: 0, level: 'D', color: '#DC2626', bg: '#FEF2F2' }
]

export function getCreditMeta(score) {
  for (const t of CREDIT_THRESHOLDS) {
    if (score >= t.min) return t
  }
  return CREDIT_THRESHOLDS[CREDIT_THRESHOLDS.length - 1]
}

export function getStageColor(stage) {
  const colors = {
    '初步接触': '#94A3B8',
    '需求沟通': '#2563EB',
    '方案制定': '#8B5CF6',
    '报价': '#F59E0B',
    '投标': '#059669',
    '签单': '#059669'
  }
  return colors[stage] || '#94A3B8'
}

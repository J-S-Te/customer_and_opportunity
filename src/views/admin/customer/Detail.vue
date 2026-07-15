<template>
  <AdminLayout>
    <template #header-actions>
      <el-button @click="$router.push('/admin/customers')">← 返回</el-button>
      <el-button type="primary" @click="$router.push(`/admin/opportunities/new?customer_id=${customer.customer_id}`)">
        <el-icon><Plus /></el-icon>新增商机
      </el-button>
      <el-button @click="$router.push(`/admin/customers/${customer.customer_id}/edit`)">编辑</el-button>
    </template>

    <div v-loading="loading" class="detail-wrap">
      <!-- 概览卡 -->
      <div class="crm-card">
        <div class="overview">
          <div class="overview-main">
            <h2 class="customer-name">
              {{ customer.customer_name }}
              <el-tag v-if="customer.credit_level" :type="creditTag(customer.credit_level)" effect="dark" size="default" style="margin-left:8px">
                {{ customer.credit_level }} · {{ customer.credit_score }}
              </el-tag>
              <el-tag v-if="customer.risk_flag" type="danger" effect="dark" style="margin-left:6px">风险预警</el-tag>
            </h2>
            <div class="customer-tags">
              <el-tag size="small">{{ customer.customer_type }}</el-tag>
              <el-tag size="small" type="info">{{ customer.industry }}</el-tag>
              <el-tag size="small" type="info">{{ customer.enterprise_scale }}</el-tag>
              <el-tag :type="statusTag(customer.customer_status)" size="small">{{ customer.customer_status }}</el-tag>
            </div>
          </div>
          <div class="overview-info">
            <div class="info-item">
              <div class="info-label">客户编号</div>
              <div class="info-value">{{ customer.customer_id }}</div>
            </div>
            <div class="info-item">
              <div class="info-label">统一社会信用代码</div>
              <div class="info-value">{{ customer.credit_code }}</div>
            </div>
            <div class="info-item">
              <div class="info-label">注册地址</div>
              <div class="info-value">{{ customer.reg_address }}</div>
            </div>
            <div class="info-item">
              <div class="info-label">最近跟进</div>
              <div class="info-value">{{ customer.last_follow_date }}</div>
            </div>
          </div>
        </div>
      </div>

      <!-- Tabs -->
      <el-tabs v-model="activeTab" class="detail-tabs">
        <el-tab-pane label="基本信息" name="basic">
          <div class="crm-card"><div class="crm-card-body">
            <h3 class="crm-card-title">业务信息</h3>
            <el-descriptions :column="2" border>
              <el-descriptions-item label="主营业务">{{ biz.main_business || '--' }}</el-descriptions-item>
              <el-descriptions-item label="测评需求类型">
                <el-tag v-for="t in biz.test_demand_type" :key="t" size="small" style="margin-right:4px">{{ t }}</el-tag>
              </el-descriptions-item>
              <el-descriptions-item label="办公地址" :span="2">{{ customer.office_address || '--' }}</el-descriptions-item>
            </el-descriptions>
          </div></div>

          <div class="crm-card"><div class="crm-card-body">
            <h3 class="crm-card-title">财务信息（脱敏）</h3>
            <el-descriptions :column="2" border>
              <el-descriptions-item label="开户名">{{ finance.account_name || '--' }}</el-descriptions-item>
              <el-descriptions-item label="开户行">{{ finance.bank_name || '--' }}</el-descriptions-item>
              <el-descriptions-item label="银行账号"><span style="font-family:monospace">{{ finance.bank_account || '--' }}</span></el-descriptions-item>
              <el-descriptions-item label="税号">{{ finance.tax_no || '--' }}</el-descriptions-item>
              <el-descriptions-item label="开票抬头" :span="2">{{ finance.invoice_title || '--' }}</el-descriptions-item>
            </el-descriptions>
          </div></div>
        </el-tab-pane>

        <el-tab-pane :label="`干系人 (${stakeholders.length})`" name="stakeholders">
          <div class="crm-card"><div class="crm-card-body">
            <div v-if="stakeholders.length === 0" class="empty-text">暂无干系人</div>
            <div v-else>
              <div v-for="s in stakeholders" :key="s.stakeholder_id" class="contact-row">
                <div class="avatar" :style="{ background: avatarColor(s.name) }">{{ s.name[0] }}</div>
                <div class="contact-main">
                  <div class="contact-name">{{ s.name }} · {{ s.position }}</div>
                  <div class="contact-meta">
                    <el-tag v-for="t in s.preference_tags" :key="t" size="small" type="info" style="margin-right:4px">{{ t }}</el-tag>
                    决策权重：<el-rate v-model="s.decision_weight" disabled :max="5" />
                  </div>
                </div>
                <div class="contact-info">
                  <div>{{ s.phone }}</div>
                  <div style="color:var(--primary);font-size:12px">{{ s.email }}</div>
                </div>
              </div>
            </div>
          </div></div>
        </el-tab-pane>

        <el-tab-pane :label="`商机 (${opportunities.length})`" name="opportunities">
          <div class="crm-card"><div class="crm-card-body">
            <el-empty v-if="opportunities.length === 0" description="暂无商机" />
            <el-table v-else :data="opportunities" @row-click="goOpp">
              <el-table-column prop="opportunity_id" label="商机编号" width="160">
                <template #default="{ row }"><span class="crm-text-link">{{ row.opportunity_id }}</span></template>
              </el-table-column>
              <el-table-column prop="opp_name" label="商机名称" min-width="200" />
              <el-table-column prop="current_stage" label="当前阶段" width="120" />
              <el-table-column prop="expected_amount" label="预计金额" width="120">
                <template #default="{ row }">{{ formatWan(row.expected_amount) }}</template>
              </el-table-column>
              <el-table-column prop="expected_sign_date" label="预计签单" width="120" />
              <el-table-column prop="sales_owner" label="负责人" width="100" />
            </el-table>
          </div></div>
        </el-tab-pane>

        <el-tab-pane label="信息系统" name="systems">
          <div class="crm-card"><div class="crm-card-body">
            <el-empty v-if="systems.length === 0" description="暂无信息系统" />
            <el-table v-else :data="systems" border>
              <el-table-column prop="system_name" label="系统名称" min-width="200" />
              <el-table-column prop="system_level" label="系统等级" width="100" />
              <el-table-column prop="deploy_mode" label="部署方式" width="120" />
              <el-table-column prop="system_count" label="数量" width="80" />
              <el-table-column prop="test_history" label="测评历史" min-width="240" />
            </el-table>
          </div></div>
        </el-tab-pane>

        <el-tab-pane label="操作日志" name="logs">
          <div class="crm-card"><div class="crm-card-body">
            <el-timeline>
              <el-timeline-item
                v-for="log in logs"
                :key="log.log_id"
                :timestamp="formatDateTime(log.op_time)"
                placement="top"
              >
                <el-card shadow="never">
                  <div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">
                    <el-tag size="small">{{ log.op_type }}</el-tag>
                    <span style="color:var(--text-muted);font-size:12px">操作人：{{ log.op_user }}</span>
                  </div>
                  <div style="font-size:13px">{{ log.change_content }}</div>
                  <div v-if="log.op_reason" style="color:var(--text-muted);font-size:12px;margin-top:4px">原因：{{ log.op_reason }}</div>
                </el-card>
              </el-timeline-item>
            </el-timeline>
          </div></div>
        </el-tab-pane>
      </el-tabs>
    </div>
  </AdminLayout>
</template>

<script setup>
import { ref, onMounted, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Plus } from '@element-plus/icons-vue'
import AdminLayout from '@/layout/AdminLayout.vue'
import { customerApi, opportunityApi } from '@/api'
import { formatDateTime, formatWan } from '@/utils/format'

const router = useRouter()
const route = useRoute()
const loading = ref(false)
const activeTab = ref('basic')
const customer = ref({})
const biz = ref({})
const stakeholders = ref([])
const systems = ref([])
const finance = ref({})
const opportunities = ref([])
const logs = ref([])

async function load() {
  loading.value = true
  try {
    const res = await customerApi.detail(route.params.id)
    customer.value = res.data.basic
    biz.value = res.data.biz_info
    stakeholders.value = res.data.stakeholders
    systems.value = res.data.systems
    finance.value = res.data.finance
    opportunities.value = res.data.opportunities
    // 日志
    const logRes = await customerApi.logs(route.params.id, { page: 1, page_size: 50 })
    logs.value = logRes.data.list
  } finally {
    loading.value = false
  }
}

function goOpp(row) {
  router.push(`/admin/opportunities/${row.opportunity_id}`)
}

function creditTag(level) {
  return { A: 'success', B: 'primary', C: 'warning', D: 'danger' }[level] || 'info'
}
function statusTag(status) {
  return { '成交': 'success', '在跟': 'primary', '潜在': 'info', '流失': 'danger', '作废': 'info' }[status] || 'info'
}
function avatarColor(name) {
  const colors = ['#2563EB', '#8B5CF6', '#F59E0B', '#059669', '#DC2626']
  let hash = 0
  for (let i = 0; i < name.length; i++) hash = (hash * 31 + name.charCodeAt(i)) % 5
  return colors[hash]
}

onMounted(load)
</script>

<style lang="scss" scoped>
.detail-wrap { display: flex; flex-direction: column; gap: 16px; }
.overview {
  display: flex;
  gap: 32px;
  padding: 24px 28px;
  flex-wrap: wrap;
}
.overview-main { flex: 1; min-width: 280px; }
.customer-name { font-size: 22px; font-weight: 700; margin: 0 0 12px; display: flex; align-items: center; }
.customer-tags { display: flex; gap: 6px; }
.overview-info {
  flex: 2;
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.info-item .info-label { font-size: 11px; color: var(--text-muted); margin-bottom: 4px; }
.info-item .info-value { font-size: 14px; font-weight: 500; }

.contact-row {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 14px;
  background: #FAFBFC;
  border-radius: var(--radius);
  margin-bottom: 10px;
}
.avatar {
  width: 44px; height: 44px;
  border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  color: #fff; font-weight: 600; font-size: 16px;
  flex-shrink: 0;
}
.contact-main { flex: 1; }
.contact-name { font-weight: 600; font-size: 14px; margin-bottom: 4px; }
.contact-meta { font-size: 12px; color: var(--text-muted); display: flex; align-items: center; gap: 4px; }
.contact-info { text-align: right; font-size: 13px; }

.detail-tabs :deep(.el-tabs__header) { background: var(--card); padding: 0 16px; border-radius: var(--radius-lg) var(--radius-lg) 0 0; border: 1px solid var(--card-border); border-bottom: none; margin-bottom: 0; }
.detail-tabs :deep(.el-tabs__content) { padding: 16px 0 0; }

.empty-text { text-align: center; color: var(--text-muted); padding: 40px; }
</style>

<template>
  <AdminLayout>
    <template #header-actions>
      <el-button @click="$router.back()">← 返回</el-button>
      <el-button @click="onExport"><el-icon><Download /></el-icon>导出 PDF</el-button>
      <el-button v-if="quote.quotation_status === '草稿'" type="primary" @click="onSubmit">提交审批</el-button>
      <el-button v-if="quote.quotation_status === '已生效'" type="success" @click="onTransfer">转合同</el-button>
    </template>

    <div v-loading="loading" class="detail-wrap">
      <!-- 概览 -->
      <div class="crm-card">
        <div class="overview">
          <div class="ov-item">
            <div class="ov-label">关联客户</div>
            <div class="ov-value">{{ quote.customer_name }}</div>
          </div>
          <div class="ov-item">
            <div class="ov-label">关联商机</div>
            <div class="ov-value crm-text-link">{{ quote.opp_name }}</div>
          </div>
          <div class="ov-item">
            <div class="ov-label">报价日期</div>
            <div class="ov-value">{{ quote.quotation_date }}</div>
          </div>
          <div class="ov-item">
            <div class="ov-label">有效期至</div>
            <div class="ov-value" :style="{ color: isExpired ? 'var(--warning)' : 'inherit' }">{{ quote.effective_end_date }}</div>
          </div>
          <div class="ov-item">
            <div class="ov-label">总金额</div>
            <div class="ov-value total">¥{{ formatWan(quote.total_amount) }}</div>
          </div>
        </div>
      </div>

      <!-- 报价明细 -->
      <div class="crm-card"><div class="crm-card-body">
        <h3 class="crm-card-title">报价明细</h3>
        <el-table :data="items" border>
          <el-table-column prop="service_item" label="服务项目" min-width="160" />
          <el-table-column prop="service_content" label="服务内容" min-width="240" />
          <el-table-column prop="quantity" label="数量" width="80" align="center" />
          <el-table-column prop="unit_price" label="单价" width="120" align="right">
            <template #default="{ row }">¥{{ Number(row.unit_price).toLocaleString() }}</template>
          </el-table-column>
          <el-table-column label="折扣" width="80" align="center">
            <template #default="{ row }">{{ (row.discount_rate * 100).toFixed(0) }}%</template>
          </el-table-column>
          <el-table-column prop="amount" label="金额" width="120" align="right">
            <template #default="{ row }"><strong>¥{{ Number(row.amount).toLocaleString() }}</strong></template>
          </el-table-column>
        </el-table>
        <div class="totals">
          <div class="total-row"><span>小计：</span><strong>¥{{ Number(quote.subtotal).toLocaleString() }}</strong></div>
          <div class="total-row"><span>折扣：</span><strong style="color:var(--accent)">-¥{{ Number(quote.discount_amount).toLocaleString() }}</strong></div>
          <div class="total-row"><span>税费：</span><strong>¥{{ Number(quote.tax_amount).toLocaleString() }}</strong></div>
          <div class="total-row total-final"><span>合计：</span><strong style="color:var(--primary);font-size:18px">¥{{ Number(quote.total_amount).toLocaleString() }}</strong></div>
        </div>
      </div></div>

      <!-- 付款条款 -->
      <div class="crm-card"><div class="crm-card-body">
        <h3 class="crm-card-title">付款条款</h3>
        <el-tag v-if="quote.payment_terms_default" type="info" style="margin-bottom:12px">使用系统默认条款</el-tag>
        <el-table :data="paymentTerms" border>
          <el-table-column prop="phase_name" label="阶段名称" />
          <el-table-column label="付款比例" width="120">
            <template #default="{ row }">{{ (row.pay_ratio * 100).toFixed(0) }}%</template>
          </el-table-column>
          <el-table-column prop="condition_desc" label="触发条件" />
          <el-table-column prop="sort_order" label="顺序" width="80" />
        </el-table>
      </div></div>

      <!-- 审批流 -->
      <div class="crm-card"><div class="crm-card-body">
        <h3 class="crm-card-title">审批流程</h3>
        <ApprovalFlow :data="approvals" />
      </div></div>

      <!-- 商务条款 -->
      <div v-if="quote.special_terms || quote.warranty_terms" class="crm-card"><div class="crm-card-body">
        <h3 class="crm-card-title">商务条款</h3>
        <el-descriptions :column="1" border>
          <el-descriptions-item label="服务周期">{{ quote.service_period }}</el-descriptions-item>
          <el-descriptions-item label="质保条款">{{ quote.warranty_terms || '--' }}</el-descriptions-item>
          <el-descriptions-item label="特殊约定">{{ quote.special_terms || '--' }}</el-descriptions-item>
        </el-descriptions>
      </div></div>
    </div>
  </AdminLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Download } from '@element-plus/icons-vue'
import AdminLayout from '@/layout/AdminLayout.vue'
import ApprovalFlow from '@/components/common/ApprovalFlow.vue'
import { quotationApi } from '@/api'
import { formatWan } from '@/utils/format'

const route = useRoute()
const router = useRouter()
const loading = ref(false)
const quote = ref({})
const items = ref([])
const paymentTerms = ref([])
const approvals = ref([])

const isExpired = computed(() => {
  return quote.value.effective_end_date && new Date(quote.value.effective_end_date) < new Date()
})

async function load() {
  loading.value = true
  try {
    const res = await quotationApi.detail(route.params.id)
    quote.value = res.data.basic
    items.value = res.data.items
    paymentTerms.value = res.data.payment_terms
    approvals.value = res.data.approvals
  } finally {
    loading.value = false
  }
}

function onExport() {
  ElMessage.success('PDF 导出已触发')
}

async function onSubmit() {
  await quotationApi.submitApproval(quote.value.quotation_id)
  ElMessage.success('已提交审批（销售总监 → 财务经理）')
  load()
}

async function onTransfer() {
  try {
    await ElMessageBox.confirm('确认推送至合同管理子系统？', '推送', { type: 'warning' })
    await quotationApi.transferToContract(quote.value.quotation_id)
    ElMessage.success('已推送')
    load()
  } catch (e) { /* cancel */ }
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
.ov-item { display: flex; flex-direction: column; gap: 4px; min-width: 120px; }
.ov-label { font-size: 11px; color: var(--text-placeholder); }
.ov-value { font-size: 14px; font-weight: 600; }
.total { font-family: monospace; color: var(--primary); font-size: 16px; }
.totals {
  margin-top: 12px;
  padding: 14px;
  background: var(--bg);
  border-radius: var(--radius);
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 6px;
}
.total-row {
  display: flex;
  gap: 16px;
  min-width: 280px;
  justify-content: space-between;
  font-size: 14px;
}
.total-final {
  padding-top: 8px;
  border-top: 1px solid var(--card-border);
}
</style>

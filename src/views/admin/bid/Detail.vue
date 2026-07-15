<template>
  <AdminLayout>
    <template #header-actions>
      <el-button @click="$router.back()">← 返回</el-button>
      <el-button v-if="bid.bid_status === '已投标' && !bid.bid_rank" type="primary" @click="showBidResultDialog = true">开标记录</el-button>
      <el-button v-if="bid.deposit.refund_status !== '已退'" @click="showRefundDialog = true">保证金退还</el-button>
    </template>

    <div v-loading="loading" class="detail-wrap">
      <!-- 概览 -->
      <div class="crm-card">
        <div class="overview">
          <div class="ov-item"><div class="ov-label">投标编号</div><div class="ov-value">{{ bid.bid_id }}</div></div>
          <div class="ov-item"><div class="ov-label">招标编号</div><div class="ov-value">{{ bid.bid_code }}</div></div>
          <div class="ov-item"><div class="ov-label">招标人</div><div class="ov-value">{{ bid.tender_name }}</div></div>
          <div class="ov-item"><div class="ov-label">代理机构</div><div class="ov-value">{{ bid.agency_name }}</div></div>
          <div class="ov-item"><div class="ov-label">投标截止</div><div class="ov-value" style="color:var(--danger)">{{ bid.bid_deadline?.slice(0, 16) }}</div></div>
          <div class="ov-item"><div class="ov-label">状态</div><div class="ov-value"><el-tag :type="statusTag(bid.bid_status)">{{ bid.bid_status }}</el-tag></div></div>
        </div>
      </div>

      <div class="detail-grid">
        <!-- 标书信息 -->
        <div class="crm-card"><div class="crm-card-body">
          <h3 class="crm-card-title">标书信息</h3>
          <el-descriptions :column="1" border>
            <el-descriptions-item label="项目名称">{{ bid.project_name }}</el-descriptions-item>
            <el-descriptions-item label="关联商机">
              <span class="crm-text-link" @click="$router.push(`/admin/opportunities/${bid.opportunity_id}`)">{{ bid.opportunity_id }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="开标时间">{{ bid.bid_result_time || '--' }}</el-descriptions-item>
            <el-descriptions-item label="中标排名">{{ bid.bid_rank || '--' }}</el-descriptions-item>
            <el-descriptions-item label="落标原因" v-if="bid.lost_reason">{{ bid.lost_reason }}</el-descriptions-item>
            <el-descriptions-item label="中标通知书" v-if="bid.winning_notice">
              <el-link :href="bid.winning_notice" target="_blank" type="primary">查看通知书</el-link>
            </el-descriptions-item>
          </el-descriptions>
        </div></div>

        <!-- 保证金 -->
        <div class="crm-card"><div class="crm-card-body">
          <h3 class="crm-card-title">保证金管理</h3>
          <el-descriptions :column="2" border>
            <el-descriptions-item label="保证金金额">
              <span style="font-family:monospace;font-weight:700;color:var(--primary)">¥{{ Number(bid.deposit?.amount || 0).toLocaleString() }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="缴纳时间">{{ bid.deposit?.pay_time || '--' }}</el-descriptions-item>
            <el-descriptions-item label="预计到期">{{ bid.deposit?.expected_refund_date || '--' }}</el-descriptions-item>
            <el-descriptions-item label="退还状态">
              <el-tag :type="depositTag(bid.deposit?.refund_status)">{{ bid.deposit?.refund_status || '未退' }}</el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="退还金额">{{ bid.deposit?.refund_amount ? '¥' + Number(bid.deposit.refund_amount).toLocaleString() : '--' }}</el-descriptions-item>
            <el-descriptions-item label="退还时间">{{ bid.deposit?.refund_time || '--' }}</el-descriptions-item>
          </el-descriptions>
        </div></div>
      </div>

      <!-- 时间线 -->
      <div class="crm-card"><div class="crm-card-body">
        <h3 class="crm-card-title">关键时间节点</h3>
        <el-steps :active="activeStep" finish-status="success" align-center>
          <el-step title="准备" :description="formatDate(bid.created_time)" />
          <el-step title="标书制作" description="进行中" />
          <el-step title="已投标" :description="bid.bid_deadline?.slice(0, 10)" />
          <el-step :title="bid.bid_status === '中标' ? '中标' : bid.bid_status === '落标' ? '落标' : '待开标'" />
        </el-steps>
      </div></div>
    </div>

    <!-- 开标对话框 -->
    <el-dialog v-model="showBidResultDialog" title="开标记录" width="500">
      <el-form :model="resultForm" label-width="100px">
        <el-form-item label="开标结果">
          <el-radio-group v-model="resultForm.bid_status">
            <el-radio-button value="中标">中标</el-radio-button>
            <el-radio-button value="落标">落标</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="排名" v-if="resultForm.bid_status === '中标'">
          <el-input-number v-model="resultForm.bid_rank" :min="1" />
        </el-form-item>
        <el-form-item label="开标时间">
          <el-date-picker v-model="resultForm.bid_result_time" type="datetime" value-format="YYYY-MM-DD HH:mm:ss" style="width:100%" />
        </el-form-item>
        <el-form-item label="落标原因" v-if="resultForm.bid_status === '落标'">
          <el-input v-model="resultForm.lost_reason" type="textarea" :rows="2" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showBidResultDialog = false">取消</el-button>
        <el-button type="primary" @click="submitBidResult">提交</el-button>
      </template>
    </el-dialog>

    <!-- 保证金退还 -->
    <el-dialog v-model="showRefundDialog" title="保证金退还登记" width="500">
      <el-form :model="refundForm" label-width="100px">
        <el-form-item label="退还金额">
          <el-input-number v-model="refundForm.refund_amount" :min="0" :precision="2" style="width:100%" />
        </el-form-item>
        <el-form-item label="退还时间">
          <el-date-picker v-model="refundForm.refund_time" type="datetime" value-format="YYYY-MM-DD HH:mm:ss" style="width:100%" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showRefundDialog = false">取消</el-button>
        <el-button type="primary" @click="submitRefund">提交</el-button>
      </template>
    </el-dialog>
  </AdminLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import AdminLayout from '@/layout/AdminLayout.vue'
import { bidApi } from '@/api'
import { formatDate } from '@/utils/format'

const route = useRoute()
const loading = ref(false)
const bid = ref({ deposit: {} })
const showBidResultDialog = ref(false)
const showRefundDialog = ref(false)

const resultForm = ref({ bid_status: '中标', bid_rank: 1, bid_result_time: '', lost_reason: '' })
const refundForm = ref({ refund_amount: 0, refund_time: '' })

const activeStep = computed(() => {
  const map = { '准备中': 1, '标书制作': 1, '已投标': 2, '中标': 3, '落标': 3 }
  return map[bid.value.bid_status] || 0
})

async function load() {
  loading.value = true
  try {
    const res = await bidApi.detail(route.params.id)
    bid.value = res.data
  } finally {
    loading.value = false
  }
}

async function submitBidResult() {
  await bidApi.recordResult(bid.value.bid_id, resultForm.value)
  ElMessage.success(`已记录开标结果：${resultForm.value.bid_status}${resultForm.value.bid_status === '中标' ? '，已推送至合同管理子系统' : ''}`)
  showBidResultDialog.value = false
  load()
}

async function submitRefund() {
  await bidApi.refundDeposit(bid.value.bid_id, refundForm.value)
  ElMessage.success('保证金退还登记成功')
  showRefundDialog.value = false
  load()
}

function statusTag(s) {
  return { '准备中': 'info', '标书制作': 'primary', '已投标': 'warning', '中标': 'success', '落标': 'danger' }[s] || 'info'
}
function depositTag(s) {
  return { '未退': 'danger', '已退': 'success', '部分退': 'warning', '待退还': 'warning', '投标中': 'info' }[s] || 'info'
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
.detail-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
</style>

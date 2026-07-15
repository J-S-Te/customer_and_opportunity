<template>
  <AdminLayout>
    <template #header-actions>
      <el-button @click="viewMode = 'kanban'" :type="viewMode === 'kanban' ? 'primary' : 'default'">
        <el-icon><Grid /></el-icon>看板
      </el-button>
      <el-button @click="viewMode = 'list'" :type="viewMode === 'list' ? 'primary' : 'default'">
        <el-icon><List /></el-icon>列表
      </el-button>
      <el-button type="primary" @click="$router.push('/admin/opportunities/new')">
        <el-icon><Plus /></el-icon>新增商机
      </el-button>
    </template>

    <div class="stat-row">
      <StatCard label="商机总数" :value="opportunities.length" suffix="个" />
      <StatCard label="预计总金额" :value="formatWan(totalAmount)" />
      <StatCard label="平均金额" :value="formatWan(avgAmount)" />
      <StatCard label="本月签单" :value="2" suffix="个" :trend="{ type: 'up', text: '+1' }" />
    </div>

    <!-- 看板视图 -->
    <div v-if="viewMode === 'kanban'" class="kanban-board">
      <div v-for="(stage, idx) in stages" :key="stage" class="kanban-col"
        @dragover.prevent @drop="onDrop($event, stage)">
        <div class="kanban-col-header" :style="{ '--stage-color': stageColors[idx] }">
          <div class="dot" />
          <span class="title">{{ stage }}</span>
          <span class="count">{{ grouped[stage]?.length || 0 }}</span>
          <span class="amount">{{ formatWan(stageAmount(stage)) }}</span>
        </div>
        <div class="kanban-col-body">
          <div
            v-for="opp in grouped[stage] || []"
            :key="opp.opportunity_id"
            class="kanban-card"
            draggable="true"
            @dragstart="onDragStart($event, opp)"
            @click="goDetail(opp)"
          >
            <div class="kc-head">
              <span class="kc-name">{{ opp.opp_name }}</span>
              <el-tag v-if="opp.below_price_flag" type="danger" size="small">低于限价</el-tag>
            </div>
            <div class="kc-customer">{{ opp.customer_name }}</div>
            <div class="kc-row">
              <span class="kc-amount">¥{{ formatWan(opp.expected_amount) }}</span>
              <span class="kc-date">预计签 {{ opp.expected_sign_date.slice(5) }}</span>
            </div>
            <div class="kc-foot">
              <div class="kc-owner">
                <div class="avatar" :style="{ background: avatarColor(opp.sales_owner) }">{{ opp.sales_owner[0] }}</div>
                {{ opp.sales_owner }}
              </div>
              <el-tag size="small" type="info">{{ opp.opp_source }}</el-tag>
            </div>
          </div>
          <div v-if="!grouped[stage]?.length" class="empty-col">暂无</div>
        </div>
      </div>
    </div>

    <!-- 列表视图 -->
    <div v-else class="crm-table-wrap">
      <el-table :data="opportunities" v-loading="loading" stripe @row-click="goDetail">
        <el-table-column prop="opportunity_id" label="商机编号" width="160">
          <template #default="{ row }"><span class="crm-text-link">{{ row.opportunity_id }}</span></template>
        </el-table-column>
        <el-table-column prop="opp_name" label="商机名称" min-width="200" />
        <el-table-column prop="customer_name" label="客户" min-width="180" />
        <el-table-column prop="current_stage" label="当前阶段" width="120">
          <template #default="{ row }">
            <el-tag size="small" :style="{ background: stageColors[stages.indexOf(row.current_stage)] + '20', color: stageColors[stages.indexOf(row.current_stage)], border: 'none' }">
              {{ row.current_stage }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="expected_amount" label="预计金额" width="120">
          <template #default="{ row }">¥{{ formatWan(row.expected_amount) }}</template>
        </el-table-column>
        <el-table-column prop="expected_sign_date" label="预计签单" width="120" />
        <el-table-column prop="sales_owner" label="负责人" width="100" />
        <el-table-column prop="opp_status" label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusTag(row.opp_status)">{{ row.opp_status }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </AdminLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, Grid, List } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import AdminLayout from '@/layout/AdminLayout.vue'
import StatCard from '@/components/common/StatCard.vue'
import { opportunityApi } from '@/api'
import { formatWan } from '@/utils/format'

const router = useRouter()
const loading = ref(false)
const opportunities = ref([])
const viewMode = ref('kanban')

const stages = ['初步接触', '需求沟通', '方案制定', '报价', '投标']
const stageColors = ['#94A3B8', '#2563EB', '#8B5CF6', '#F59E0B', '#059669']

const grouped = computed(() => {
  const result = {}
  stages.forEach(s => { result[s] = [] })
  opportunities.value.forEach(o => {
    if (result[o.current_stage]) result[o.current_stage].push(o)
  })
  return result
})

const totalAmount = computed(() =>
  opportunities.value.reduce((s, o) => s + (o.expected_amount || 0), 0)
)
const avgAmount = computed(() =>
  opportunities.value.length ? totalAmount.value / opportunities.value.length : 0
)

function stageAmount(stage) {
  return (grouped.value[stage] || []).reduce((s, o) => s + (o.expected_amount || 0), 0)
}

async function load() {
  loading.value = true
  try {
    const res = await opportunityApi.list({ page: 1, page_size: 100 })
    opportunities.value = res.data.list
  } finally {
    loading.value = false
  }
}

function goDetail(opp) {
  router.push(`/admin/opportunities/${opp.opportunity_id}`)
}

function statusTag(status) {
  return { '跟进中': 'primary', '已签单': 'success', '已流失': 'danger', '已作废': 'info' }[status] || 'info'
}

function avatarColor(name) {
  const colors = ['#2563EB', '#3B82F6', '#8B5CF6', '#F59E0B', '#059669', '#DC2626']
  let hash = 0
  for (let i = 0; i < name.length; i++) hash = (hash * 31 + name.charCodeAt(i)) % 6
  return colors[hash]
}

// 拖拽
let dragOpp = null
function onDragStart(e, opp) {
  dragOpp = opp
  e.dataTransfer.effectAllowed = 'move'
}
async function onDrop(e, stage) {
  if (!dragOpp || dragOpp.current_stage === stage) return
  const oldStage = dragOpp.current_stage
  try {
    await ElMessageBox.confirm(`确认将商机「${dragOpp.opp_name}」从「${oldStage}」推进到「${stage}」？关键阶段推进需要销售总监审批`, '阶段推进', { type: 'info' })
    // 模拟调用后端
    dragOpp.current_stage = stage
    await opportunityApi.advanceStage(dragOpp.opportunity_id, { to_stage: stage, key_result: '阶段推进' })
    ElMessage.success(`已推进到「${stage}」，已触发销售总监审批`)
  } catch (err) { /* cancel */ }
  dragOpp = null
}

onMounted(load)
</script>

<style lang="scss" scoped>
.stat-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}
.kanban-board {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 12px;
  min-height: 600px;
}
.kanban-col {
  background: #F1F5FD;
  border-radius: var(--radius-md);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 200px;
}
.kanban-col-header {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px;
  .dot {
    width: 8px; height: 8px; border-radius: 50%;
    background: var(--stage-color);
  }
  .title { font-weight: 600; font-size: 13px; }
  .count {
    background: #fff; padding: 0 6px; border-radius: 8px;
    font-size: 11px; font-weight: 600; color: var(--text-muted);
  }
  .amount { margin-left: auto; font-size: 11px; color: var(--text-muted); }
}
.kanban-col-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  flex: 1;
  min-height: 100px;
}
.kanban-card {
  background: #fff;
  border: 1px solid var(--card-border);
  border-radius: var(--radius);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  cursor: grab;
  transition: all 0.15s;
  box-shadow: var(--shadow-sm);
  &:hover {
    box-shadow: var(--shadow-md);
    transform: translateY(-1px);
  }
  &:active { cursor: grabbing; }
}
.kc-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
}
.kc-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.kc-customer { font-size: 11px; color: var(--text-muted); }
.kc-row { display: flex; justify-content: space-between; align-items: center; }
.kc-amount {
  font-family: monospace; font-size: 14px; font-weight: 700; color: var(--primary);
}
.kc-date { font-size: 11px; color: var(--text-placeholder); }
.kc-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 4px;
}
.kc-owner {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--text-secondary);
  .avatar {
    width: 18px; height: 18px; border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    color: #fff; font-size: 10px; font-weight: 600;
  }
}
.empty-col {
  text-align: center;
  padding: 24px;
  color: var(--text-placeholder);
  font-size: 12px;
  border: 1px dashed var(--card-border);
  border-radius: var(--radius);
}
</style>

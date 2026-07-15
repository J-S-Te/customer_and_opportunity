<template>
  <AdminLayout>
    <template #header-actions>
      <el-button type="primary" @click="$router.push('/admin/customers/new')">
        <el-icon><Plus /></el-icon>新建客户
      </el-button>
    </template>

    <div class="stat-row">
      <StatCard label="全部客户" :value="stats.total" suffix="家" :trend="{ type: 'up', text: '+12%' }" />
      <StatCard label="跟进中" :value="stats.following" suffix="家" :trend="{ type: 'up', text: '+8%' }" />
      <StatCard label="高价值客户" :value="stats.high_value" suffix="家" :trend="{ type: 'up', text: '+5%' }" />
      <StatCard label="待跟进" :value="stats.to_follow" suffix="家" />
    </div>

    <SearchBar
      placeholder="搜索客户名称、编号..."
      :filters="searchFilters"
      @search="onSearch"
    >
      <template #extra>
        <el-button @click="onExport"><el-icon><Download /></el-icon>导出</el-button>
      </template>
    </SearchBar>

    <div class="crm-table-wrap">
      <el-table :data="filteredList" v-loading="loading" stripe @row-click="goDetail" :row-class-name="riskRowClass">
        <el-table-column prop="customer_id" label="客户编号" width="160">
          <template #default="{ row }">
            <span class="crm-text-link">{{ row.customer_id }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="customer_name" label="客户名称" min-width="200">
          <template #default="{ row }">
            <span style="font-weight:600">{{ row.customer_name }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="customer_type" label="客户类型" width="100" />
        <el-table-column prop="industry" label="所属行业" width="100" />
        <el-table-column prop="credit_level" label="信用等级" width="100">
          <template #default="{ row }">
            <el-tag :type="creditTag(row.credit_level)" size="small" effect="dark">{{ row.credit_level }}</el-tag>
            <el-tooltip v-if="row.credit_score" :content="`评分 ${row.credit_score}`">
              <span class="score-tip">{{ row.credit_score }}</span>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column prop="customer_status" label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="statusTag(row.customer_status)" size="small">{{ row.customer_status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="last_follow_date" label="最近跟进" width="120">
          <template #default="{ row }">
            <span class="crm-text-muted">{{ row.last_follow_date || '--' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click.stop="goDetail(row)">查看</el-button>
            <el-button link type="primary" @click.stop="goEdit(row)">编辑</el-button>
            <el-dropdown @command="(cmd) => onAction(cmd, row)" @click.stop>
              <el-button link type="primary">更多<el-icon><ArrowDown /></el-icon></el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="merge">合并客户</el-dropdown-item>
                  <el-dropdown-item command="credit">调整信用</el-dropdown-item>
                  <el-dropdown-item command="archive" divided>作废（软删）</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>
      </el-table>
      <div class="pagination">
        <el-pagination
          v-model:current-page="pagination.page"
          v-model:page-size="pagination.page_size"
          :total="list.length"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next, jumper"
          @size-change="loadList"
          @current-change="loadList"
        />
      </div>
    </div>
  </AdminLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Download, ArrowDown } from '@element-plus/icons-vue'
import AdminLayout from '@/layout/AdminLayout.vue'
import StatCard from '@/components/common/StatCard.vue'
import SearchBar from '@/components/common/SearchBar.vue'
import { customerApi } from '@/api'
import { ENUMS } from '@/mock'

const router = useRouter()
const loading = ref(false)
const list = ref([])
const stats = ref({ total: 0, following: 0, high_value: 0, to_follow: 0 })
const pagination = ref({ page: 1, page_size: 20 })

const searchFilters = [
  { field: 'customer_type', label: '客户类型', options: ENUMS.customer_type.map(v => ({ label: v, value: v })) },
  { field: 'industry', label: '行业', options: ENUMS.industry.map(v => ({ label: v, value: v })) },
  { field: 'credit_level', label: '信用等级', options: ENUMS.credit_level.map(v => ({ label: v, value: v })) },
  { field: 'customer_status', label: '状态', options: ENUMS.customer_status.map(v => ({ label: v.label, value: v.value })) }
]

const filteredList = computed(() => list.value)

async function loadList() {
  loading.value = true
  try {
    const res = await customerApi.list({
      page: pagination.value.page,
      page_size: pagination.value.page_size
    })
    list.value = res.data.list
  } finally {
    loading.value = false
  }
}

async function loadStats() {
  try {
    const res = await customerApi.list({ page: 1, page_size: 100 })
    const all = res.data.list
    stats.value = {
      total: res.data.pagination.total,
      following: all.filter(c => c.customer_status === '在跟').length,
      high_value: all.filter(c => c.credit_level === 'A').length,
      to_follow: all.filter(c => c.customer_status === '潜在').length
    }
  } catch { /* noop */ }
}

function onSearch(params) {
  // 简化：本地过滤
  // 实际环境应该走接口
  customerApi.list({ page: 1, page_size: 100 }).then(res => {
    let result = res.data.list
    if (params.keyword) {
      const kw = params.keyword.toLowerCase()
      result = result.filter(c => c.customer_name.toLowerCase().includes(kw) || c.customer_id.toLowerCase().includes(kw))
    }
    Object.keys(params).forEach(k => {
      if (k !== 'keyword' && params[k]) {
        result = result.filter(c => c[k] === params[k])
      }
    })
    list.value = result
  })
}

function goDetail(row) {
  router.push(`/admin/customers/${row.customer_id}`)
}
function goEdit(row) {
  router.push(`/admin/customers/${row.customer_id}/edit`)
}

function onExport() {
  ElMessage.info('导出功能已触发（mock）')
}

async function onAction(cmd, row) {
  if (cmd === 'archive') {
    try {
      await ElMessageBox.confirm(`确定要作废客户「${row.customer_name}」吗？作废后数据保留 6 年`, '提示', { type: 'warning' })
      ElMessage.success('作废成功')
    } catch (e) { /* cancel */ }
  } else if (cmd === 'merge') {
    ElMessage.info('合并客户（请选择目标客户）')
  } else if (cmd === 'credit') {
    ElMessage.info('信用等级调整')
  }
}

function creditTag(level) {
  return { A: 'success', B: 'primary', C: 'warning', D: 'danger' }[level] || 'info'
}
function statusTag(status) {
  return { '成交': 'success', '在跟': 'primary', '潜在': 'info', '流失': 'danger', '作废': 'info' }[status] || 'info'
}
function riskRowClass({ row }) {
  return row.credit_level === 'D' || row.risk_flag ? 'risk-row' : ''
}

onMounted(() => {
  loadList()
  loadStats()
})
</script>

<style lang="scss" scoped>
.stat-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}
.pagination {
  display: flex;
  justify-content: flex-end;
  padding: 14px 16px;
}
:deep(.risk-row) {
  background: #FFF7ED !important;
  &:hover td { background: #FEF3C7 !important; }
}
.score-tip {
  margin-left: 4px;
  font-size: 11px;
  color: var(--text-muted);
}
</style>

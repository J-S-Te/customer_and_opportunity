<template>
  <AdminLayout>
    <template #header-actions>
      <el-button type="primary" @click="$router.push('/admin/quotations/new')">
        <el-icon><Plus /></el-icon>新建报价
      </el-button>
    </template>

    <SearchBar
      placeholder="搜索报价单号、商机名称..."
      :filters="filters"
      @search="onSearch"
    />

    <div class="crm-table-wrap">
      <el-table :data="filteredList" v-loading="loading" stripe @row-click="goDetail">
        <el-table-column prop="quotation_id" label="报价单号" width="160">
          <template #default="{ row }"><span class="crm-text-link">{{ row.quotation_id }}</span></template>
        </el-table-column>
        <el-table-column prop="opp_name" label="关联商机" min-width="200">
          <template #default="{ row }"><span style="font-weight:600">{{ row.opp_name }}</span></template>
        </el-table-column>
        <el-table-column prop="customer_name" label="客户" width="180" />
        <el-table-column prop="total_amount" label="总金额" width="120">
          <template #default="{ row }">
            <span style="font-family:monospace;font-weight:700;color:var(--primary)">¥{{ formatWan(row.total_amount) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="quotation_status" label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="statusTag(row.quotation_status)" size="small">{{ row.quotation_status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="quotation_date" label="报价日期" width="120" />
        <el-table-column prop="effective_end_date" label="有效期至" width="120">
          <template #default="{ row }">
            <span :class="{ 'expired': isExpired(row.effective_end_date) }">{{ row.effective_end_date }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click.stop="goDetail(row)">查看</el-button>
            <el-button v-if="row.quotation_status === '草稿'" link type="primary">编辑</el-button>
            <el-button v-if="row.quotation_status === '已生效'" link type="success" @click.stop="onTransfer(row)">转合同</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="pagination">
        <el-pagination
          v-model:current-page="pagination.page"
          v-model:page-size="pagination.page_size"
          :total="list.length"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next"
        />
      </div>
    </div>
  </AdminLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import AdminLayout from '@/layout/AdminLayout.vue'
import SearchBar from '@/components/common/SearchBar.vue'
import { quotationApi } from '@/api'
import { formatWan } from '@/utils/format'

const router = useRouter()
const loading = ref(false)
const list = ref([])
const pagination = ref({ page: 1, page_size: 20 })

const filters = [
  { field: 'quotation_status', label: '审批状态', options: [
    { value: '草稿', label: '草稿' }, { value: '审批中', label: '审批中' },
    { value: '已生效', label: '已生效' }, { value: '已失效', label: '已失效' },
    { value: '已转合同', label: '已转合同' }
  ] }
]

const filteredList = computed(() => list.value)

async function load() {
  loading.value = true
  try {
    const res = await quotationApi.list({ page: 1, page_size: 100 })
    list.value = res.data.list
  } finally {
    loading.value = false
  }
}

function onSearch(params) {
  quotationApi.list({ page: 1, page_size: 100 }).then(res => {
    let result = res.data.list
    if (params.keyword) {
      const kw = params.keyword.toLowerCase()
      result = result.filter(q => q.quotation_id.toLowerCase().includes(kw) || q.opp_name.toLowerCase().includes(kw))
    }
    if (params.quotation_status) result = result.filter(q => q.quotation_status === params.quotation_status)
    list.value = result
  })
}

function goDetail(row) {
  router.push(`/admin/quotations/${row.quotation_id}`)
}

function isExpired(date) {
  return date && new Date(date) < new Date()
}

function statusTag(s) {
  return { '草稿': 'info', '审批中': 'primary', '已生效': 'success', '已失效': 'info', '已转合同': 'warning' }[s] || 'info'
}

async function onTransfer(row) {
  try {
    await ElMessageBox.confirm(`确认将报价单「${row.quotation_id}」推送至合同管理子系统？`, '推送确认', { type: 'warning' })
    await quotationApi.transferToContract(row.quotation_id)
    ElMessage.success('已推送至合同管理子系统')
    load()
  } catch (e) { /* cancel */ }
}

onMounted(load)
</script>

<style lang="scss" scoped>
.pagination {
  display: flex;
  justify-content: flex-end;
  padding: 14px 16px;
}
.expired { color: var(--text-placeholder); text-decoration: line-through; }
</style>

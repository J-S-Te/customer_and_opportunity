<template>
  <AdminLayout>
    <template #header-actions>
      <el-button type="primary" @click="$router.push('/admin/bids/new')">
        <el-icon><Plus /></el-icon>新建投标
      </el-button>
    </template>

    <div class="stat-row">
      <StatCard label="投标项目总数" :value="stats.total || 0" suffix="个" />
      <StatCard label="在投项目" :value="stats.active || 0" suffix="个" />
      <StatCard label="中标率" :value="(stats.win_rate * 100 || 0) + '%'" />
      <StatCard label="待退保证金" :value="'¥' + formatWan(stats.pending_deposit)" />
    </div>

    <SearchBar
      placeholder="搜索项目名称、招标编号..."
      :filters="filters"
      @search="onSearch"
    />

    <div class="crm-table-wrap">
      <el-table :data="filteredList" v-loading="loading" stripe @row-click="goDetail">
        <el-table-column prop="bid_id" label="投标编号" width="160">
          <template #default="{ row }"><span class="crm-text-link">{{ row.bid_id }}</span></template>
        </el-table-column>
        <el-table-column prop="project_name" label="项目名称" min-width="240">
          <template #default="{ row }"><span style="font-weight:600">{{ row.project_name }}</span></template>
        </el-table-column>
        <el-table-column prop="bid_code" label="招标编号" width="160" />
        <el-table-column prop="tender_name" label="招标人" width="160" />
        <el-table-column prop="bid_deadline" label="投标截止" width="160">
          <template #default="{ row }">
            <span :style="{ color: isNearDeadline(row.bid_deadline) ? 'var(--danger)' : 'inherit' }">
              {{ row.bid_deadline?.slice(0, 10) }}
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="deposit.amount" label="保证金" width="120">
          <template #default="{ row }">¥{{ formatWan(row.deposit.amount) }}</template>
        </el-table-column>
        <el-table-column prop="bid_status" label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="statusTag(row.bid_status)" size="small">{{ row.bid_status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click.stop="goDetail(row)">查看</el-button>
            <el-button v-if="row.bid_status === '已投标' && !row.bid_rank" link type="warning" @click.stop="onBidOpen(row)">开标</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </AdminLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { Plus } from '@element-plus/icons-vue'
import AdminLayout from '@/layout/AdminLayout.vue'
import StatCard from '@/components/common/StatCard.vue'
import SearchBar from '@/components/common/SearchBar.vue'
import { bidApi } from '@/api'
import { formatWan } from '@/utils/format'

const router = useRouter()
const loading = ref(false)
const list = ref([])
const stats = ref({})
const filters = [
  { field: 'bid_status', label: '投标状态', options: [
    { value: '准备中', label: '准备中' }, { value: '标书制作', label: '标书制作' },
    { value: '已投标', label: '已投标' }, { value: '中标', label: '中标' }, { value: '落标', label: '落标' }
  ] }
]
const filteredList = computed(() => list.value)

async function load() {
  loading.value = true
  try {
    const [listRes, statsRes] = await Promise.all([
      bidApi.list({ page: 1, page_size: 100 }),
      bidApi.stats()
    ])
    list.value = listRes.data.list
    stats.value = statsRes.data
  } finally {
    loading.value = false
  }
}

function onSearch(params) {
  bidApi.list({ page: 1, page_size: 100 }).then(res => {
    let result = res.data.list
    if (params.keyword) {
      const kw = params.keyword.toLowerCase()
      result = result.filter(b => b.project_name.toLowerCase().includes(kw) || b.bid_code.toLowerCase().includes(kw))
    }
    if (params.bid_status) result = result.filter(b => b.bid_status === params.bid_status)
    list.value = result
  })
}

function goDetail(row) { router.push(`/admin/bids/${row.bid_id}`) }
function onBidOpen(row) { router.push(`/admin/bids/${row.bid_id}?action=open`) }

function statusTag(s) {
  return { '准备中': 'info', '标书制作': 'primary', '已投标': 'warning', '中标': 'success', '落标': 'danger' }[s] || 'info'
}
function isNearDeadline(d) {
  if (!d) return false
  const diff = (new Date(d) - Date.now()) / (1000 * 60 * 60 * 24)
  return diff < 3
}

onMounted(load)
</script>

<style lang="scss" scoped>
.stat-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}
</style>

<template>
  <PortalLayout>
    <div class="projects-page">
      <div class="page-head">
        <h2>我的项目</h2>
        <el-input v-model="keyword" placeholder="搜索项目名称..." style="width:280px" clearable />
      </div>

      <div class="project-grid">
        <div v-for="p in filtered" :key="p.project_id" class="project-card">
          <div class="project-head">
            <span class="project-id">{{ p.project_id }}</span>
            <el-tag :type="statusTag(p.project_status)" size="small">{{ p.project_status }}</el-tag>
          </div>
          <h3 class="project-name">{{ p.project_name }}</h3>
          <div class="project-meta">
            <div><el-icon><Document /></el-icon> 合同：{{ p.contract_id }}</div>
            <div><el-icon><Calendar /></el-icon> 预计完成：{{ p.expected_end_date }}</div>
            <div><el-icon><Loading /></el-icon> 当前阶段：{{ p.current_stage }}</div>
          </div>
          <div class="project-progress">
            <div class="progress-row">
              <span>进度</span>
              <strong>{{ p.progress_pct }}%</strong>
            </div>
            <el-progress :percentage="p.progress_pct" :stroke-width="8" :show-text="false"
              :status="p.delayed_flag ? 'exception' : 'success'" />
          </div>
          <div v-if="p.delayed_flag" class="delayed-tag">
            <el-icon><Warning /></el-icon> 项目延期
          </div>
          <div class="project-actions">
            <el-button size="small" type="primary" @click="viewProgress(p)">查看进度</el-button>
            <el-button size="small" @click="$router.push('/portal/reports')">申请报告</el-button>
          </div>
        </div>
      </div>

      <el-empty v-if="filtered.length === 0" description="暂无项目" />
    </div>
  </PortalLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { Document, Calendar, Loading, Warning } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import PortalLayout from '@/layout/PortalLayout.vue'
import { portalApi } from '@/api'

const keyword = ref('')
const projects = ref([])

async function load() {
  const res = await portalApi.projects()
  projects.value = res.data.list
}

const filtered = computed(() => {
  if (!keyword.value) return projects.value
  return projects.value.filter(p => p.project_name.includes(keyword.value))
})

function statusTag(s) {
  return { '进行中': 'primary', '已完成': 'success', '方案制定中': 'info', '延期': 'danger' }[s] || 'info'
}

async function viewProgress(p) {
  const res = await portalApi.projectProgress(p.project_id)
  ElMessage.info(`${p.project_name}：当前进度 ${res.data.progress_pct}%（${res.data.current_stage}）`)
}

onMounted(load)
</script>

<style lang="scss" scoped>
.projects-page {
  padding: 28px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  h2 { margin: 0; font-size: 20px; font-weight: 700; }
}
.project-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 16px;
}
.project-card {
  background: var(--card);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-lg);
  padding: 20px;
  transition: all 0.15s;
  &:hover {
    box-shadow: var(--shadow-md);
    transform: translateY(-2px);
  }
}
.project-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.project-id {
  font-family: monospace;
  font-size: 12px;
  color: var(--primary);
  background: var(--primary-bg);
  padding: 2px 8px;
  border-radius: 4px;
}
.project-name {
  font-size: 16px;
  font-weight: 600;
  margin: 8px 0 12px;
}
.project-meta {
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 13px;
  color: var(--text-secondary);
  margin-bottom: 16px;
  div {
    display: flex;
    align-items: center;
    gap: 6px;
    .el-icon { font-size: 14px; color: var(--text-muted); }
  }
}
.project-progress {
  margin-bottom: 12px;
}
.progress-row {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  color: var(--text-muted);
  margin-bottom: 6px;
}
.delayed-tag {
  background: var(--danger-bg);
  color: var(--danger);
  padding: 6px 10px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 12px;
}
.project-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}
</style>

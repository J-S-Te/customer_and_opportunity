<template>
  <PortalLayout>
    <div class="dashboard">
      <!-- 欢迎条 -->
      <div class="welcome-card">
        <div class="welcome-text">
          <h2>欢迎回来，{{ customerName }}</h2>
          <p>您当前有 <strong>{{ dashboard.active_projects }}</strong> 个进行中的测评项目，<strong>{{ dashboard.pending_reports }}</strong> 份报告待下载</p>
        </div>
        <div class="welcome-actions">
          <el-button type="primary" @click="$router.push('/portal/projects')">查看我的项目</el-button>
        </div>
      </div>

      <!-- 统计卡片 -->
      <div class="stat-row">
        <div class="stat-card blue">
          <div class="stat-label">进行中项目</div>
          <div class="stat-value">{{ dashboard.active_projects }}</div>
        </div>
        <div class="stat-card green">
          <div class="stat-label">已完成项目</div>
          <div class="stat-value">{{ dashboard.completed_projects }}</div>
        </div>
        <div class="stat-card orange">
          <div class="stat-label">报告待下载</div>
          <div class="stat-value">{{ dashboard.pending_reports }}</div>
        </div>
        <div class="stat-card purple">
          <div class="stat-label">待服务评价</div>
          <div class="stat-value">{{ dashboard.pending_evaluations }}</div>
        </div>
      </div>

      <div class="dashboard-grid">
        <!-- 近期项目 -->
        <div class="crm-card"><div class="crm-card-body">
          <div class="card-head">
            <h3 class="crm-card-title">近期项目</h3>
            <a class="crm-text-link" @click="$router.push('/portal/projects')">查看全部 →</a>
          </div>
          <div v-for="p in dashboard.recent_projects || []" :key="p.project_id" class="project-row">
            <div class="project-color" :style="{ background: projectColor(p.current_stage) }" />
            <div class="project-info">
              <div class="project-name">
                <span style="font-weight:600">{{ p.project_name }}</span>
                <el-tag size="small">{{ p.current_stage }}</el-tag>
              </div>
              <div class="project-meta">
                合同编号：{{ p.contract_id }} · 预计完成：{{ p.expected_end_date }}
              </div>
              <el-progress :percentage="p.progress_pct" :stroke-width="6" :show-text="false" />
              <div style="font-size:11px;color:var(--text-muted);margin-top:4px">进度 {{ p.progress_pct }}%</div>
            </div>
          </div>
        </div></div>

        <!-- 快捷操作 -->
        <div class="crm-card"><div class="crm-card-body">
          <h3 class="crm-card-title">快捷操作</h3>
          <div class="action-list">
            <div class="action-btn" @click="$router.push('/portal/projects')">
              <div class="action-dot" style="background:var(--primary)" />
              <span>项目进度查询</span>
            </div>
            <div class="action-btn" @click="$router.push('/portal/reports')">
              <div class="action-dot" style="background:var(--accent)" />
              <span>电子报告申请</span>
            </div>
            <div class="action-btn" @click="$router.push('/portal/filing')">
              <div class="action-dot" style="background:var(--purple)" />
              <span>备案信息填写</span>
            </div>
            <div class="action-btn" @click="$router.push('/portal/evaluation')">
              <div class="action-dot" style="background:var(--warning)" />
              <span>服务评价</span>
            </div>
            <div class="action-btn" @click="$router.push('/portal/feedback')">
              <div class="action-dot" style="background:var(--danger)" />
              <span>反馈与投诉</span>
            </div>
          </div>
        </div></div>
      </div>
    </div>
  </PortalLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import PortalLayout from '@/layout/PortalLayout.vue'
import { portalApi } from '@/api'
import { useUserStore } from '@/store/user'

const userStore = useUserStore()
const customerName = computed(() => userStore.portalUser?.customer_name?.slice(0, 8) || '客户')

const dashboard = ref({})
async function load() {
  const res = await portalApi.dashboard()
  dashboard.value = res.data
}
function projectColor(stage) {
  const map = {
    '方案制定': '#2563EB',
    '已交付': '#059669',
    '测试执行': '#F59E0B'
  }
  return map[stage] || '#94A3B8'
}

onMounted(load)
</script>

<style lang="scss" scoped>
.dashboard {
  padding: 28px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.welcome-card {
  background: linear-gradient(135deg, #2563EB 0%, #1D4ED8 100%);
  color: #fff;
  padding: 24px 28px;
  border-radius: var(--radius-lg);
  display: flex;
  align-items: center;
  gap: 24px;
}
.welcome-text h2 { font-size: 20px; margin: 0 0 4px; }
.welcome-text p { margin: 0; opacity: 0.9; font-size: 13px; }
.welcome-actions { margin-left: auto; }
.welcome-actions .el-button { background: rgba(255,255,255,0.15); border: 1px solid rgba(255,255,255,0.3); color: #fff; &:hover { background: rgba(255,255,255,0.25); } }

.stat-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}
.stat-card {
  background: var(--card);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-lg);
  padding: 20px;
  position: relative;
  overflow: hidden;
  &::before {
    content: '';
    position: absolute;
    top: 0;
    left: 0;
    width: 4px;
    height: 100%;
  }
  &.blue::before { background: var(--primary); }
  &.green::before { background: var(--accent); }
  &.orange::before { background: var(--warning); }
  &.purple::before { background: var(--purple); }
}
.stat-label { font-size: 12px; color: var(--text-muted); margin-bottom: 6px; }
.stat-value { font-size: 28px; font-weight: 700; line-height: 1.2; }

.dashboard-grid {
  display: grid;
  grid-template-columns: 2fr 1fr;
  gap: 16px;
}
.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
  .crm-card-title { margin: 0; }
}
.project-row {
  display: flex;
  gap: 14px;
  padding: 16px;
  background: var(--bg);
  border-radius: var(--radius);
  margin-bottom: 12px;
  &:last-child { margin-bottom: 0; }
}
.project-color { width: 4px; border-radius: 2px; }
.project-info { flex: 1; }
.project-name {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
}
.project-meta { font-size: 12px; color: var(--text-muted); margin-bottom: 8px; }

.action-list { display: flex; flex-direction: column; gap: 8px; }
.action-btn {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  background: var(--bg);
  border-radius: var(--radius);
  cursor: pointer;
  font-size: 14px;
  font-weight: 500;
  transition: all 0.15s;
  &:hover {
    background: var(--primary-bg);
    color: var(--primary);
    transform: translateX(4px);
  }
}
.action-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
</style>

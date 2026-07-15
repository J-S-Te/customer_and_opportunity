<template>
  <PortalLayout>
    <div class="reports-page">
      <h2>报告中心</h2>

      <div class="reports-grid">
        <!-- 申请表单 -->
        <div class="crm-card"><div class="crm-card-body">
          <h3 class="crm-card-title">申请电子报告</h3>
          <el-form :model="requestForm" label-width="100px">
            <el-form-item label="选择项目" required>
              <el-select v-model="requestForm.project_id" placeholder="请选择已完成项目" style="width:100%">
                <el-option v-for="p in projects" :key="p.project_id" :label="p.project_name" :value="p.project_id" />
              </el-select>
            </el-form-item>
            <el-form-item label="报告类型" required>
              <el-select v-model="requestForm.report_type" placeholder="请选择" style="width:100%">
                <el-option v-for="t in reportTypes" :key="t" :label="t" :value="t" />
              </el-select>
            </el-form-item>
            <el-form-item label="申请原因">
              <el-input v-model="requestForm.request_reason" type="textarea" :rows="2" placeholder="请简要说明（可选）" />
            </el-form-item>
            <el-form-item label="接收邮箱" required>
              <el-input v-model="requestForm.receive_email" placeholder="将用于接收加密下载链接" />
            </el-form-item>
            <el-alert type="info" :closable="false" show-icon style="margin: 12px 0">
              <template #title>安全提示</template>
              报告将采用 <strong>AES-256</strong> 加密，下载链接有效期 72 小时。
              国密 SM4 算法预留支持。
            </el-alert>
            <el-button type="primary" size="large" :loading="submitting" style="width:100%" @click="onSubmit">
              提交申请
            </el-button>
          </el-form>
        </div></div>

        <!-- 申请记录 -->
        <div class="crm-card"><div class="crm-card-body">
          <h3 class="crm-card-title">申请与下载记录</h3>
          <el-empty v-if="requests.length === 0" description="暂无申请记录" />
          <div v-else>
            <div v-for="r in requests" :key="r.request_id" class="request-row" :class="r.request_status">
              <div class="request-head">
                <div>
                  <div style="font-weight:600">{{ r.file_name || '待生成报告' }}</div>
                  <div style="color:var(--text-muted);font-size:12px;margin-top:4px">
                    {{ r.report_type }} · {{ formatDateTime(r.submit_time) }}
                  </div>
                </div>
                <el-tag :type="statusTag(r.request_status)" size="small">{{ r.request_status }}</el-tag>
              </div>
              <div class="request-actions">
                <el-button v-if="r.request_status === '已发放'" type="primary" size="small" @click="onDownload(r)">
                  <el-icon><Download /></el-icon>下载加密报告
                </el-button>
                <span v-if="r.link_expire_time && r.request_status === '已发放'" style="font-size:11px;color:var(--warning)">
                  有效期至 {{ formatDateTime(r.link_expire_time) }}
                </span>
              </div>
            </div>
          </div>
        </div></div>
      </div>
    </div>
  </PortalLayout>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { Download } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import PortalLayout from '@/layout/PortalLayout.vue'
import { portalApi } from '@/api'
import { formatDateTime } from '@/utils/format'

const projects = ref([])
const requests = ref([])
const submitting = ref(false)
const reportTypes = ['测评报告', '整改建议', '合规证明']

const requestForm = ref({
  project_id: '',
  report_type: '',
  request_reason: '',
  receive_email: ''
})

async function load() {
  const [pRes, rRes] = await Promise.all([
    portalApi.projects(),
    portalApi.reportList()
  ])
  projects.value = pRes.data.list
  requests.value = rRes.data.list
}

async function onSubmit() {
  if (!requestForm.value.project_id || !requestForm.value.report_type || !requestForm.value.receive_email) {
    ElMessage.warning('请填写完整申请信息')
    return
  }
  submitting.value = true
  try {
    await portalApi.reportRequest(requestForm.value)
    ElMessage.success('申请已提交，项目经理审批通过后将通过邮件发送加密下载链接')
    requestForm.value = { project_id: '', report_type: '', request_reason: '', receive_email: '' }
    load()
  } finally {
    submitting.value = false
  }
}

function onDownload(r) {
  ElMessage.success(`开始下载：${r.file_name}（mock）`)
}

function statusTag(s) {
  return { '待审批': 'info', '已通过': 'success', '已驳回': 'danger', '已发放': 'success', '已过期': 'info' }[s] || 'info'
}

onMounted(load)
</script>

<style lang="scss" scoped>
.reports-page {
  padding: 28px;
  display: flex;
  flex-direction: column;
  gap: 20px;
  h2 { margin: 0; font-size: 20px; font-weight: 700; }
}
.reports-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.request-row {
  background: var(--bg);
  border-radius: var(--radius);
  padding: 14px;
  margin-bottom: 10px;
  border-left: 3px solid var(--primary);
  &.已发放 { border-color: var(--accent); background: var(--accent-bg); }
  &.待审批 { border-color: var(--warning); }
  &.已驳回, &.已过期 { border-color: var(--text-muted); opacity: 0.7; }
}
.request-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 8px;
}
.request-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  justify-content: space-between;
}
</style>

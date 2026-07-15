<template>
  <PortalLayout>
    <div class="feedback-page">
      <h2>反馈与投诉</h2>

      <div class="feedback-grid">
        <!-- 提交反馈 -->
        <div class="crm-card"><div class="crm-card-body">
          <h3 class="crm-card-title">提交反馈</h3>
          <el-form :model="form" label-width="100px">
            <el-form-item label="反馈类型" required>
              <el-radio-group v-model="form.feedback_type">
                <el-radio-button value="异议">异议</el-radio-button>
                <el-radio-button value="投诉">投诉</el-radio-button>
                <el-radio-button value="建议">建议</el-radio-button>
              </el-radio-group>
            </el-form-item>
            <el-form-item label="关联项目">
              <el-select v-model="form.project_id" placeholder="可选" clearable style="width:100%">
                <el-option v-for="p in projects" :key="p.project_id" :label="p.project_name" :value="p.project_id" />
              </el-select>
            </el-form-item>
            <el-form-item label="问题描述" required>
              <el-input v-model="form.content" type="textarea" :rows="5" placeholder="请详细描述您的问题或建议..." maxlength="1000" show-word-limit />
            </el-form-item>
            <el-form-item label="附件">
              <el-upload action="/api/files/upload" :auto-upload="false" multiple>
                <el-button>选择文件</el-button>
                <template #tip>
                  <div style="font-size:12px;color:var(--text-muted)">支持图片、PDF，单文件不超过 20MB</div>
                </template>
              </el-upload>
            </el-form-item>
            <el-alert type="warning" :closable="false" show-icon style="margin: 12px 0">
              <template #title>响应承诺</template>
              反馈将在 <strong>24 小时内</strong>响应，超时自动升级至管理层
            </el-alert>
            <el-button type="primary" size="large" :loading="submitting" style="width:100%" @click="onSubmit">
              提交反馈
            </el-button>
          </el-form>
        </div></div>

        <!-- 历史反馈 -->
        <div class="crm-card"><div class="crm-card-body">
          <h3 class="crm-card-title">历史反馈</h3>
          <el-empty v-if="history.length === 0" description="暂无反馈记录" />
          <div v-else>
            <div v-for="f in history" :key="f.feedback_id" class="fb-row">
              <div class="fb-head">
                <el-tag :type="typeTag(f.feedback_type)" size="small">{{ f.feedback_type }}</el-tag>
                <span class="fb-time">{{ formatDateTime(f.submit_time) }}</span>
                <el-tag :type="statusTag(f.handle_status)" size="small" style="margin-left:auto">{{ f.handle_status }}</el-tag>
                <el-tag v-if="f.escalated_flag" type="danger" size="small">已升级</el-tag>
              </div>
              <div class="fb-content">{{ f.content }}</div>
              <div v-if="f.reply_content" class="fb-reply">
                <div style="font-weight:600;color:var(--accent);font-size:12px;margin-bottom:4px">
                  处理人 {{ f.handler }} 的回复 ({{ formatDateTime(f.reply_time) }})
                </div>
                <div>{{ f.reply_content }}</div>
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
import { ElMessage } from 'element-plus'
import PortalLayout from '@/layout/PortalLayout.vue'
import { portalApi } from '@/api'
import { formatDateTime } from '@/utils/format'

const projects = ref([])
const history = ref([])
const submitting = ref(false)

const form = ref({
  feedback_type: '建议',
  project_id: '',
  content: ''
})

async function load() {
  const res = await portalApi.projects()
  projects.value = res.data.list
  // 历史反馈简化为静态展示
  history.value = [{
    feedback_id: '1',
    feedback_type: '建议',
    submit_time: '2026-06-10 14:00:00',
    handle_status: '已回复',
    handler: '客户成功部-李婷芳',
    reply_content: '感谢您的建议，我们已排期至 7 月份上线该功能。',
    reply_time: '2026-06-11 10:00:00',
    escalated_flag: false,
    content: '希望增加定期进度报告推送功能，每周一发送项目进度邮件'
  }]
}

async function onSubmit() {
  if (!form.value.content) {
    ElMessage.warning('请填写问题描述')
    return
  }
  submitting.value = true
  try {
    await portalApi.submitFeedback(form.value)
    ElMessage.success('反馈已提交，我们将在 24 小时内处理')
    form.value.content = ''
    load()
  } finally {
    submitting.value = false
  }
}

function typeTag(t) {
  return { '异议': 'warning', '投诉': 'danger', '建议': 'info' }[t] || 'info'
}
function statusTag(s) {
  return { '待处理': 'info', '处理中': 'primary', '已回复': 'success', '已关闭': 'info' }[s] || 'info'
}

onMounted(load)
</script>

<style lang="scss" scoped>
.feedback-page {
  padding: 28px;
  display: flex;
  flex-direction: column;
  gap: 20px;
  h2 { margin: 0; font-size: 20px; font-weight: 700; }
}
.feedback-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.fb-row {
  background: var(--bg);
  border-radius: var(--radius);
  padding: 14px;
  margin-bottom: 12px;
}
.fb-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  .fb-time { font-size: 11px; color: var(--text-muted); }
}
.fb-content {
  font-size: 13px;
  line-height: 1.6;
  margin-bottom: 8px;
}
.fb-reply {
  background: var(--accent-bg);
  border-left: 3px solid var(--accent);
  padding: 10px 12px;
  border-radius: 4px;
  font-size: 13px;
}
</style>

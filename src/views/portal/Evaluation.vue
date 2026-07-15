<template>
  <PortalLayout>
    <div class="eval-page">
      <div class="eval-card">
        <div class="eval-head">
          <h2>服务评价</h2>
          <p>请对本次合作进行评价，您的反馈将帮助我们持续改进</p>
        </div>

        <el-form :model="form" label-width="100px">
          <el-form-item label="选择项目" required>
            <el-select v-model="form.project_id" placeholder="请选择" style="width:100%">
              <el-option v-for="p in completedProjects" :key="p.project_id" :label="p.project_name" :value="p.project_id" />
            </el-select>
          </el-form-item>

          <el-form-item label="专业能力" required>
            <el-rate v-model="form.prof_score" :max="5" show-text :texts="['极差', '失望', '一般', '满意', '惊喜']" />
          </el-form-item>
          <el-form-item label="响应速度" required>
            <el-rate v-model="form.response_score" :max="5" show-text />
          </el-form-item>
          <el-form-item label="服务态度" required>
            <el-rate v-model="form.attitude_score" :max="5" show-text />
          </el-form-item>
          <el-form-item label="报告质量" required>
            <el-rate v-model="form.report_score" :max="5" show-text />
          </el-form-item>

          <el-form-item label="综合得分">
            <div class="score-display">
              <div class="big-score">{{ avgScore.toFixed(1) }}</div>
              <el-rate v-model="avgScoreDisplay" disabled :max="5" :show-text="false" />
              <span :class="['score-tag', avgScore < 3 ? 'low' : 'ok']">
                {{ avgScore < 3 ? '⚠️ 低分将自动推送管理层' : '感谢您的肯定' }}
              </span>
            </div>
          </el-form-item>

          <el-form-item label="评语">
            <el-input v-model="form.comment" type="textarea" :rows="4" placeholder="请留下您的宝贵建议（可选）" maxlength="500" show-word-limit />
          </el-form-item>

          <el-form-item label="匿名提交">
            <el-switch v-model="form.anonymous" />
            <span style="margin-left:8px;color:var(--text-muted);font-size:12px">开启后将隐藏您的账号信息</span>
          </el-form-item>

          <el-button type="primary" size="large" :loading="submitting" style="width:100%;margin-top:16px" @click="onSubmit">
            提交评价
          </el-button>
        </el-form>
      </div>
    </div>
  </PortalLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import PortalLayout from '@/layout/PortalLayout.vue'
import { portalApi } from '@/api'

const router = useRouter()
const submitting = ref(false)
const completedProjects = ref([])

const form = ref({
  project_id: '',
  prof_score: 5,
  response_score: 5,
  attitude_score: 5,
  report_score: 5,
  comment: '',
  anonymous: true
})

const avgScore = computed(() => {
  const sum = form.value.prof_score + form.value.response_score + form.value.attitude_score + form.value.report_score
  return sum / 4
})

const avgScoreDisplay = computed(() => Math.round(avgScore.value))

async function load() {
  const res = await portalApi.projects()
  completedProjects.value = res.data.list.filter(p => p.project_status === '已完成')
  if (completedProjects.value[0]) form.value.project_id = completedProjects.value[0].project_id
}

async function onSubmit() {
  if (!form.value.project_id) {
    ElMessage.warning('请选择项目')
    return
  }
  submitting.value = true
  try {
    const res = await portalApi.submitEvaluation(form.value)
    ElMessage.success(`评价已提交，综合得分 ${res.data.avg_score}`)
    router.push('/portal/dashboard')
  } finally {
    submitting.value = false
  }
}

onMounted(load)
</script>

<style lang="scss" scoped>
.eval-page {
  padding: 40px 28px;
  display: flex;
  justify-content: center;
}
.eval-card {
  width: 100%;
  max-width: 640px;
  background: var(--card);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-lg);
  padding: 40px;
}
.eval-head {
  text-align: center;
  margin-bottom: 32px;
  h2 { margin: 0 0 8px; font-size: 22px; font-weight: 700; }
  p { color: var(--text-muted); margin: 0; font-size: 13px; }
}
.score-display {
  display: flex;
  align-items: center;
  gap: 12px;
}
.big-score {
  font-size: 32px;
  font-weight: 800;
  color: var(--primary);
  font-family: monospace;
}
.score-tag {
  font-size: 12px;
  padding: 4px 10px;
  border-radius: 4px;
  &.low { background: var(--danger-bg); color: var(--danger); }
  &.ok { background: var(--accent-bg); color: var(--accent); }
}
</style>

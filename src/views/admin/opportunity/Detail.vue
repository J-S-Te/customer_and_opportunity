<template>
  <AdminLayout>
    <template #header-actions>
      <el-button @click="$router.back()">← 返回</el-button>
      <el-button type="primary" @click="showAdvanceDialog = true">阶段推进</el-button>
      <el-button @click="showFollowDialog = true"><el-icon><ChatDotRound /></el-icon>新增跟进</el-button>
      <el-dropdown @command="onMore">
        <el-button>更多<el-icon><ArrowDown /></el-icon></el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="transfer">转合同（推下游）</el-dropdown-item>
            <el-dropdown-item command="lost" divided>标记流失</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </template>

    <div v-loading="loading" class="detail-wrap">
      <!-- 阶段进度条 -->
      <div class="crm-card stage-card">
        <div class="stage-bar">
          <div v-for="(s, idx) in stages" :key="s" class="stage-item">
            <div class="stage-dot" :class="{
              done: idx < currentStageIdx,
              current: idx === currentStageIdx,
              pending: idx > currentStageIdx
            }">
              <el-icon v-if="idx < currentStageIdx"><Check /></el-icon>
              <span v-else>{{ idx + 1 }}</span>
            </div>
            <div class="stage-label" :class="{ current: idx === currentStageIdx }">{{ s }}</div>
            <div v-if="idx < stages.length - 1" class="stage-line" :class="idx < currentStageIdx ? 'done' : 'pending'" />
          </div>
        </div>
      </div>

      <div class="detail-grid">
        <!-- 左侧：商机详情 -->
        <div class="detail-col">
          <div class="crm-card"><div class="crm-card-body">
            <div class="card-head">
              <h3 class="crm-card-title">商机信息</h3>
              <div>
                <el-tag :type="statusTag(opp.opp_status)">{{ opp.opp_status }}</el-tag>
              </div>
            </div>
            <el-descriptions :column="1" border>
              <el-descriptions-item label="商机编号">{{ opp.opportunity_id }}</el-descriptions-item>
              <el-descriptions-item label="商机名称">{{ opp.opp_name }}</el-descriptions-item>
              <el-descriptions-item label="关联客户">
                <span class="crm-text-link" @click="$router.push(`/admin/customers/${opp.customer_id}`)">
                  {{ opp.customer_name }}
                </span>
              </el-descriptions-item>
              <el-descriptions-item label="商机来源">{{ opp.opp_source }}</el-descriptions-item>
              <el-descriptions-item label="商机类型">{{ opp.opp_type }}</el-descriptions-item>
              <el-descriptions-item label="预计金额"><span style="color:var(--primary);font-weight:700;font-family:monospace">¥{{ formatWan(opp.expected_amount) }}</span></el-descriptions-item>
              <el-descriptions-item label="预计签单">{{ opp.expected_sign_date }}</el-descriptions-item>
              <el-descriptions-item label="销售负责人">{{ opp.sales_owner }}</el-descriptions-item>
              <el-descriptions-item label="支持人员">
                <el-tag v-for="m in opp.support_team" :key="m" size="small" style="margin-right:4px">{{ m }}</el-tag>
              </el-descriptions-item>
            </el-descriptions>
          </div></div>

          <div v-if="opp.competitor_info?.length" class="crm-card"><div class="crm-card-body">
            <h3 class="crm-card-title">竞争信息</h3>
            <div v-for="c in opp.competitor_info" :key="c.name" class="competitor-row">
              <div class="competitor-name">⚔️ {{ c.name }}</div>
              <div class="competitor-meta">
                <div><strong>优势：</strong>{{ c.advantage }}</div>
                <div><strong>劣势：</strong>{{ c.disadvantage }}</div>
                <div style="color:var(--primary)"><strong>我方策略：</strong>{{ c.our_strategy }}</div>
              </div>
            </div>
          </div></div>
        </div>

        <!-- 右侧：跟进记录 + 团队 -->
        <div class="detail-col">
          <div class="crm-card"><div class="crm-card-body">
            <div class="card-head">
              <h3 class="crm-card-title">跟进记录 ({{ follows.length }})</h3>
              <el-button size="small" @click="showFollowDialog = true">+ 新增跟进</el-button>
            </div>
            <el-timeline>
              <el-timeline-item
                v-for="f in follows"
                :key="f.follow_id"
                :timestamp="formatDateTime(f.follow_time)"
                placement="top"
                :color="f.stage_after !== f.stage_before ? '#2563EB' : '#94A3B8'"
              >
                <el-card shadow="never">
                  <div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">
                    <el-tag size="small">{{ f.follow_type }}</el-tag>
                    <el-tag v-if="f.stage_after !== f.stage_before" type="primary" size="small">
                      {{ f.stage_before }} → {{ f.stage_after }}
                    </el-tag>
                  </div>
                  <div style="font-size:13px;line-height:1.6">{{ f.content }}</div>
                  <div v-if="f.customer_feedback" style="margin-top:6px;color:var(--text-secondary);font-size:12px">
                    💬 客户反馈：{{ f.customer_feedback }}
                  </div>
                  <div v-if="f.next_follow_time" style="margin-top:6px;color:var(--warning);font-size:12px">
                    📅 下次跟进：{{ formatDateTime(f.next_follow_time) }}
                  </div>
                  <div v-if="f.key_result" style="margin-top:6px;color:var(--accent);font-size:12px">
                    🎯 关键成果：{{ f.key_result }}
                  </div>
                </el-card>
              </el-timeline-item>
            </el-timeline>
          </div></div>

          <div class="crm-card"><div class="crm-card-body">
            <h3 class="crm-card-title">项目团队</h3>
            <div class="team-row">
              <div class="avatar" style="background:#2563EB">{{ opp.sales_owner?.[0] }}</div>
              <div>
                <div style="font-weight:600">{{ opp.sales_owner }} · 销售负责人</div>
                <div style="color:var(--text-muted);font-size:12px">测评事业部 · 客户经理</div>
              </div>
            </div>
            <div v-for="m in opp.support_team" :key="m" class="team-row">
              <div class="avatar" style="background:#8B5CF6">{{ m[0] }}</div>
              <div>
                <div style="font-weight:600">{{ m }} · 项目支持</div>
                <div style="color:var(--text-muted);font-size:12px">技术支持 / 商务支持</div>
              </div>
            </div>
          </div></div>
        </div>
      </div>
    </div>

    <!-- 跟进弹窗 -->
    <el-dialog v-model="showFollowDialog" title="新增跟进记录" width="600">
      <el-form :model="followForm" label-width="100px">
        <el-form-item label="跟进方式">
          <el-select v-model="followForm.follow_type">
            <el-option v-for="t in ['上门拜访','电话','邮件','会议','站内消息']" :key="t" :label="t" :value="t" />
          </el-select>
        </el-form-item>
        <el-form-item label="沟通内容">
          <el-input v-model="followForm.content" type="textarea" :rows="4" placeholder="本次沟通详情..." />
        </el-form-item>
        <el-form-item label="客户反馈">
          <el-input v-model="followForm.customer_feedback" placeholder="客户的态度与反馈" />
        </el-form-item>
        <el-form-item label="下次跟进">
          <el-date-picker v-model="followForm.next_follow_time" type="datetime" value-format="YYYY-MM-DD HH:mm:ss" style="width:100%" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showFollowDialog = false">取消</el-button>
        <el-button type="primary" @click="submitFollow">提交</el-button>
      </template>
    </el-dialog>

    <!-- 阶段推进弹窗 -->
    <el-dialog v-model="showAdvanceDialog" title="阶段推进" width="500">
      <el-alert type="info" :closable="false" show-icon style="margin-bottom:16px">
        阶段推进需销售总监审批（必经节点）
      </el-alert>
      <el-form :model="advanceForm" label-width="100px">
        <el-form-item label="目标阶段">
          <el-select v-model="advanceForm.to_stage">
            <el-option v-for="s in stages" :key="s" :label="s" :value="s" />
          </el-select>
        </el-form-item>
        <el-form-item label="关键成果">
          <el-input v-model="advanceForm.key_result" type="textarea" :rows="2" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showAdvanceDialog = false">取消</el-button>
        <el-button type="primary" @click="submitAdvance">提交推进</el-button>
      </template>
    </el-dialog>
  </AdminLayout>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ChatDotRound, ArrowDown, Check } from '@element-plus/icons-vue'
import AdminLayout from '@/layout/AdminLayout.vue'
import { opportunityApi } from '@/api'
import { formatDateTime, formatWan } from '@/utils/format'

const route = useRoute()
const router = useRouter()
const loading = ref(false)
const opp = ref({})
const follows = ref([])
const showFollowDialog = ref(false)
const showAdvanceDialog = ref(false)

const stages = ['初步接触', '需求沟通', '方案制定', '报价', '投标']
const currentStageIdx = computed(() => stages.indexOf(opp.value.current_stage))

const followForm = ref({
  follow_type: '电话',
  content: '',
  customer_feedback: '',
  next_follow_time: ''
})

const advanceForm = ref({ to_stage: '', key_result: '' })

async function load() {
  loading.value = true
  try {
    const res = await opportunityApi.detail(route.params.id)
    opp.value = res.data.basic
    follows.value = res.data.follows
  } finally {
    loading.value = false
  }
}

async function submitFollow() {
  await opportunityApi.addFollow(opp.value.opportunity_id, {
    ...followForm.value,
    stage_before: opp.value.current_stage,
    stage_after: opp.value.current_stage
  })
  ElMessage.success('跟进记录已添加')
  showFollowDialog.value = false
  followForm.value = { follow_type: '电话', content: '', customer_feedback: '', next_follow_time: '' }
  load()
}

async function submitAdvance() {
  if (!advanceForm.value.to_stage) {
    ElMessage.warning('请选择目标阶段')
    return
  }
  await opportunityApi.advanceStage(opp.value.opportunity_id, advanceForm.value)
  ElMessage.success(`已推进至「${advanceForm.value.to_stage}」，审批通知已发出`)
  showAdvanceDialog.value = false
  load()
}

async function onMore(cmd) {
  if (cmd === 'transfer') {
    try {
      await ElMessageBox.confirm('确认将商机推送至合同管理子系统？推送后将创建合同草稿', '推送确认', { type: 'warning' })
      const res = await opportunityApi.transferToContract(opp.value.opportunity_id)
      ElMessage.success(`已推送，合同管理子系统返回：${res.data.contract_id}`)
    } catch (e) { /* cancel */ }
  } else if (cmd === 'lost') {
    try {
      const { value } = await ElMessageBox.prompt('请输入流失原因', '标记流失', {
        inputPlaceholder: '价格 / 技术 / 关系 / 客户预算 / 竞争对手 / 其他'
      })
      ElMessage.success('商机已标记流失')
    } catch (e) { /* cancel */ }
  }
}

function statusTag(s) {
  return { '跟进中': 'primary', '已签单': 'success', '已流失': 'danger', '已作废': 'info' }[s] || 'info'
}

onMounted(load)
</script>

<style lang="scss" scoped>
.detail-wrap { display: flex; flex-direction: column; gap: 16px; }
.stage-card { padding: 24px 28px; }
.stage-bar {
  display: flex;
  align-items: center;
}
.stage-item {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  position: relative;
}
.stage-dot {
  width: 36px; height: 36px;
  border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  font-weight: 700;
  background: #E4ECFC;
  color: var(--text-placeholder);
  z-index: 2;
  &.done { background: var(--accent); color: #fff; }
  &.current { background: var(--primary); color: #fff; box-shadow: 0 0 0 4px rgba(37,99,235,0.2); }
}
.stage-label {
  font-size: 12px;
  margin-top: 6px;
  color: var(--text-muted);
  &.current { color: var(--primary); font-weight: 700; }
}
.stage-line {
  position: absolute;
  top: 18px;
  left: 50%;
  width: 100%;
  height: 2px;
  background: #E4ECFC;
  z-index: 1;
  &.done { background: var(--accent); }
}

.detail-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.detail-col { display: flex; flex-direction: column; gap: 16px; }

.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
  .crm-card-title { margin: 0; }
}

.competitor-row {
  background: #FEF2F2;
  border-left: 3px solid var(--danger);
  padding: 12px 14px;
  border-radius: 6px;
  margin-bottom: 8px;
  &:last-child { margin-bottom: 0; }
  .competitor-name { font-weight: 600; color: var(--danger); margin-bottom: 6px; font-size: 13px; }
  .competitor-meta { font-size: 12px; line-height: 1.7; color: var(--text-secondary); }
}

.team-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px solid var(--card-border);
  &:last-child { border-bottom: none; }
  .avatar {
    width: 40px; height: 40px;
    border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    color: #fff; font-weight: 600;
  }
}
</style>

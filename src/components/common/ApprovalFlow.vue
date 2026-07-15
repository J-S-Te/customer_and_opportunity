<template>
  <el-empty v-if="!data || data.length === 0" description="暂无数据" :image-size="80" />
  <div v-else class="approval-flow">
    <div
      v-for="(node, idx) in data"
      :key="node.approval_id || idx"
      class="approval-node"
      :class="getStatusClass(node.approve_status)"
    >
      <div class="approval-check">
        <el-icon v-if="node.approve_status === '通过'"><Check /></el-icon>
        <el-icon v-else-if="node.approve_status === '驳回'"><Close /></el-icon>
        <span v-else>{{ idx + 1 }}</span>
      </div>
      <div class="approval-info">
        <div class="approval-head">
          <span class="approval-name">{{ getNodeName(node, idx) }}</span>
          <span class="approval-status" :class="getStatusClass(node.approve_status)">{{ node.approve_status }}</span>
        </div>
        <div v-if="node.approver_user" class="approval-meta">
          {{ node.approver_user }}
          <span v-if="node.approve_time"> · {{ formatDateTime(node.approve_time) }}</span>
        </div>
        <div v-if="node.approve_opinion" class="approval-opinion">"{{ node.approve_opinion }}"</div>
        <div v-else-if="node.approve_status === '待审批'" class="approval-waiting">
          <el-icon><Clock /></el-icon> 等待审批中...
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { Check, Close, Clock } from '@element-plus/icons-vue'
import { formatDateTime } from '@/utils/format'

defineProps({
  data: { type: Array, default: () => [] }
})

function getNodeName(node, idx) {
  if (node.node_name) return node.node_name
  const names = ['销售总监审批', '财务经理审批', '分管领导审批']
  return names[idx] || `审批节点${idx + 1}`
}
function getStatusClass(status) {
  return { done: status === '通过', reject: status === '驳回', pending: status === '待审批' }
}
</script>

<style lang="scss" scoped>
.approval-flow { display: flex; flex-direction: column; gap: 10px; }
.approval-node {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px 16px;
  border-radius: var(--radius);
  border: 1px solid var(--card-border);
  background: #FAFBFC;
  transition: all 0.15s;
  &.done { background: var(--accent-bg); border-color: #A7F3D0; }
  &.reject { background: var(--danger-bg); border-color: #FECACA; }
  &.pending { background: var(--warning-bg); border-color: #FDE68A; }
}
.approval-check {
  width: 32px; height: 32px; border-radius: 50%;
  background: #fff; display: flex; align-items: center; justify-content: center;
  font-weight: 700; color: var(--text-muted);
  border: 2px solid var(--card-border); flex-shrink: 0;
  .done & { background: var(--accent); color: #fff; border-color: var(--accent); }
  .reject & { background: var(--danger); color: #fff; border-color: var(--danger); }
  .pending & { background: var(--warning); color: #fff; border-color: var(--warning); }
}
.approval-info { flex: 1; min-width: 0; }
.approval-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 4px; }
.approval-name { font-weight: 600; font-size: 14px; }
.approval-status {
  font-size: 11px; font-weight: 600; padding: 2px 8px; border-radius: 4px;
  &.done { background: var(--accent); color: #fff; }
  &.reject { background: var(--danger); color: #fff; }
  &.pending { background: var(--warning); color: #fff; }
}
.approval-meta { color: var(--text-muted); font-size: 12px; margin-bottom: 6px; }
.approval-opinion {
  font-size: 13px; color: var(--text-secondary); line-height: 1.5;
  padding-left: 8px; border-left: 2px solid var(--card-border);
}
.approval-waiting { color: var(--warning); font-size: 12px; display: flex; align-items: center; gap: 4px; }
</style>

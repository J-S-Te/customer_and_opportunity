<template>
  <div class="stat-card" :class="{ clickable: clickable }" @click="onClick">
    <div class="stat-label">{{ label }}</div>
    <div class="stat-value">
      <span class="value">{{ value }}</span>
      <span v-if="suffix" class="suffix">{{ suffix }}</span>
    </div>
    <div v-if="trend" class="stat-trend" :class="trend.type">
      <el-icon v-if="trend.type === 'up'"><CaretTop /></el-icon>
      <el-icon v-else-if="trend.type === 'down'"><CaretBottom /></el-icon>
      <el-icon v-else><Minus /></el-icon>
      <span>{{ trend.text }}</span>
    </div>
  </div>
</template>

<script setup>
import { CaretTop, CaretBottom, Minus } from '@element-plus/icons-vue'

defineProps({
  label: String,
  value: [String, Number],
  suffix: String,
  trend: { type: Object, default: null },
  clickable: { type: Boolean, default: false }
})
const emit = defineEmits(['click'])
function onClick() { emit('click') }
</script>

<style lang="scss" scoped>
.stat-card {
  background: var(--card);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-lg);
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  transition: all 0.15s;
  &.clickable {
    cursor: pointer;
    &:hover { box-shadow: var(--shadow-md); transform: translateY(-2px); }
  }
}
.stat-label { font-size: 12px; color: var(--text-muted); }
.stat-value {
  font-size: 28px;
  font-weight: 700;
  line-height: 1.2;
  display: flex;
  align-items: baseline;
  gap: 4px;
  .suffix { font-size: 14px; color: var(--text-muted); font-weight: 500; }
}
.stat-trend {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 11px;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: 4px;
  align-self: flex-start;
  &.up { background: var(--accent-bg); color: var(--accent); }
  &.down { background: var(--danger-bg); color: var(--danger); }
  &.stable { background: var(--primary-bg); color: var(--primary); }
  .el-icon { font-size: 10px; }
}
</style>

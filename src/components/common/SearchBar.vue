<template>
  <div class="search-bar">
    <div class="search-input">
      <el-icon class="prefix-icon"><Search /></el-icon>
      <input v-model="model.keyword" :placeholder="placeholder" @keyup.enter="emitSearch" />
    </div>
    <el-select
      v-for="filter in filters"
      :key="filter.field"
      v-model="model[filter.field]"
      :placeholder="filter.label"
      clearable
      class="search-select"
      @change="emitSearch"
    >
      <el-option
        v-for="opt in filter.options"
        :key="opt.value"
        :label="opt.label"
        :value="opt.value"
      />
    </el-select>
    <div class="flex-spacer" />
    <el-button type="primary" @click="emitSearch"><el-icon><Search /></el-icon>搜索</el-button>
    <el-button v-if="resetable" @click="reset"><el-icon><RefreshLeft /></el-icon>重置</el-button>
    <slot name="extra" />
  </div>
</template>

<script setup>
import { reactive } from 'vue'
import { Search, RefreshLeft } from '@element-plus/icons-vue'

const props = defineProps({
  placeholder: { type: String, default: '请输入关键词...' },
  filters: { type: Array, default: () => [] },
  resetable: { type: Boolean, default: true }
})
const emit = defineEmits(['search'])
const model = reactive({ keyword: '' })
props.filters.forEach(f => { model[f.field] = '' })
function emitSearch() { emit('search', { ...model }) }
function reset() {
  Object.keys(model).forEach(k => { model[k] = '' })
  emitSearch()
}
</script>

<style lang="scss" scoped>
.search-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  background: var(--card);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-md);
  flex-wrap: wrap;
}
.search-input {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 7px 10px;
  background: var(--bg);
  border-radius: var(--radius-sm);
  min-width: 220px;
  flex: 1;
  .prefix-icon { color: var(--text-placeholder); font-size: 14px; }
  input {
    flex: 1; border: none; outline: none; background: transparent;
    font-size: 13px; color: var(--text);
    &::placeholder { color: var(--text-placeholder); }
  }
}
.search-select { width: 160px; }
.flex-spacer { flex: 1; }
</style>

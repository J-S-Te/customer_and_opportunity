<template>
  <PortalLayout>
    <div class="filing-page">
      <div class="wizard-card">
        <div class="page-head">
          <h2>等级保护备案信息填写</h2>
          <el-tag :type="filing.filing_status === '已提交' ? 'success' : 'warning'">
            {{ filing.filing_status || '暂存' }}
          </el-tag>
        </div>

        <!-- 步骤条 -->
        <div class="step-wizard">
          <div v-for="(s, idx) in steps" :key="s.title" class="step-item">
            <div class="step-dot" :class="idx === currentStep ? 'active' : (idx < currentStep ? 'done' : 'inactive')">
              <el-icon v-if="idx < currentStep"><Check /></el-icon>
              <span v-else>{{ idx + 1 }}</span>
            </div>
            <span class="step-title" :class="{ active: idx === currentStep }">{{ s.title }}</span>
            <div v-if="idx < steps.length - 1" class="step-line" :class="idx < currentStep ? 'active' : 'inactive'" />
          </div>
        </div>

        <el-progress :percentage="progressPct" :show-text="false" :stroke-width="4" style="margin-bottom: 24px" />

        <!-- 表单 -->
        <component :is="currentFormComponent" v-model="filing" />

        <div class="step-actions">
          <el-button @click="onSave" v-if="filing.filing_status !== '已提交'">
            <el-icon><Document /></el-icon>暂存
          </el-button>
          <div style="flex:1" />
          <el-button @click="prev" :disabled="currentStep === 0">上一步</el-button>
          <el-button v-if="currentStep < steps.length - 1" type="primary" @click="next">下一步</el-button>
          <el-button v-else type="success" :loading="submitting" @click="onFinalSubmit">提交备案</el-button>
        </div>
      </div>
    </div>
  </PortalLayout>
</template>

<script setup>
import { ref, computed, onMounted, h } from 'vue'
import { Check, Document } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import PortalLayout from '@/layout/PortalLayout.vue'
import { portalApi } from '@/api'
import {
  CUSTOMER_TYPES, INDUSTRIES, UNIT_NATURES, SYSTEM_LEVELS
} from '@/utils/constants'

const steps = [
  { title: '系统基本信息', key: 'system' },
  { title: '单位信息', key: 'unit' },
  { title: '责任人员', key: 'persons' },
  { title: '系统信息', key: 'sysinfo' },
  { title: '测评信息', key: 'test' }
]

const currentStep = ref(0)
const submitting = ref(false)

const filing = ref({
  filing_status: '暂存',
  system_name: '',
  filing_code: '',
  security_level: '',
  level_date: '',
  unit_name: '',
  credit_code: '',
  unit_nature: '',
  industry: '',
  address: '',
  contact_person: '',
  contact_phone: '',
  security_chief: '',
  sys_admin: '',
  net_admin: '',
  server_count: 0,
  main_apps: '',
  security_devices: '',
  test_org: '',
  test_time: '',
  test_conclusion: ''
})

const progressPct = computed(() => Math.round(((currentStep.value + 1) / steps.length) * 100))

// 各步骤表单组件
const FormSystem = {
  props: ['modelValue'],
  setup(props, { emit }) {
    const update = (k, v) => emit('update:modelValue', { ...props.modelValue, [k]: v })
    return () => h('div', { class: 'step-form' }, [
      h('el-form-item', { label: '系统名称' }, [
        h('el-input', { modelValue: props.modelValue.system_name, 'onUpdate:modelValue': v => update('system_name', v), placeholder: '请输入系统名称' })
      ]),
      h('el-form-item', { label: '备案编号' }, [
        h('el-input', { modelValue: props.modelValue.filing_code, 'onUpdate:modelValue': v => update('filing_code', v), placeholder: '请输入备案编号' })
      ]),
      h('el-form-item', { label: '安全保护等级' }, [
        h('el-select', {
          modelValue: props.modelValue.security_level,
          'onUpdate:modelValue': v => update('security_level', v),
          placeholder: '请选择',
          style: 'width:100%'
        }, () => SYSTEM_LEVELS.map(l => h('el-option', { key: l, label: l, value: l })))
      ]),
      h('el-form-item', { label: '系统定级时间' }, [
        h('el-date-picker', { modelValue: props.modelValue.level_date, 'onUpdate:modelValue': v => update('level_date', v), type: 'date', valueFormat: 'YYYY-MM-DD', style: 'width:100%' })
      ])
    ])
  }
}

const FormUnit = {
  props: ['modelValue'],
  setup(props, { emit }) {
    const update = (k, v) => emit('update:modelValue', { ...props.modelValue, [k]: v })
    return () => h('div', { class: 'step-form' }, [
      h('el-form-item', { label: '单位名称' }, [
        h('el-input', { modelValue: props.modelValue.unit_name, 'onUpdate:modelValue': v => update('unit_name', v) })
      ]),
      h('el-form-item', { label: '统一社会信用代码' }, [
        h('el-input', { modelValue: props.modelValue.credit_code, 'onUpdate:modelValue': v => update('credit_code', v), maxlength: 18 })
      ]),
      h('el-form-item', { label: '单位性质' }, [
        h('el-select', {
          modelValue: props.modelValue.unit_nature,
          'onUpdate:modelValue': v => update('unit_nature', v),
          placeholder: '请选择', style: 'width:100%'
        }, () => UNIT_NATURES.map(n => h('el-option', { key: n, label: n, value: n })))
      ]),
      h('el-form-item', { label: '所属行业' }, [
        h('el-select', {
          modelValue: props.modelValue.industry,
          'onUpdate:modelValue': v => update('industry', v),
          placeholder: '请选择', style: 'width:100%'
        }, () => INDUSTRIES.map(n => h('el-option', { key: n, label: n, value: n })))
      ]),
      h('el-form-item', { label: '地址' }, [
        h('el-input', { modelValue: props.modelValue.address, 'onUpdate:modelValue': v => update('address', v) })
      ]),
      h('el-form-item', { label: '联系人' }, [
        h('el-input', { modelValue: props.modelValue.contact_person, 'onUpdate:modelValue': v => update('contact_person', v) })
      ]),
      h('el-form-item', { label: '联系电话' }, [
        h('el-input', { modelValue: props.modelValue.contact_phone, 'onUpdate:modelValue': v => update('contact_phone', v) })
      ])
    ])
  }
}

const FormPersons = {
  props: ['modelValue'],
  setup(props, { emit }) {
    const update = (k, v) => emit('update:modelValue', { ...props.modelValue, [k]: v })
    return () => h('div', { class: 'step-form' }, [
      h('el-alert', { type: 'info', closable: false, showIcon: true, style: 'margin-bottom: 16px' }, {
        title: () => '格式要求：姓名 + 职务 + 联系电话（如：张三 · 安全总监 · 13800138000）'
      }),
      h('el-form-item', { label: '安全负责人' }, [
        h('el-input', { modelValue: props.modelValue.security_chief, 'onUpdate:modelValue': v => update('security_chief', v), placeholder: '姓名 · 职务 · 电话' })
      ]),
      h('el-form-item', { label: '系统管理员' }, [
        h('el-input', { modelValue: props.modelValue.sys_admin, 'onUpdate:modelValue': v => update('sys_admin', v), placeholder: '姓名 · 职务 · 电话' })
      ]),
      h('el-form-item', { label: '网络管理员' }, [
        h('el-input', { modelValue: props.modelValue.net_admin, 'onUpdate:modelValue': v => update('net_admin', v), placeholder: '姓名 · 职务 · 电话' })
      ])
    ])
  }
}

const FormSysinfo = {
  props: ['modelValue'],
  setup(props, { emit }) {
    const update = (k, v) => emit('update:modelValue', { ...props.modelValue, [k]: v })
    return () => h('div', { class: 'step-form' }, [
      h('el-form-item', { label: '服务器数量' }, [
        h('el-input-number', { modelValue: props.modelValue.server_count, 'onUpdate:modelValue': v => update('server_count', v), min: 0, style: 'width:100%' })
      ]),
      h('el-form-item', { label: '主要应用' }, [
        h('el-input', { modelValue: props.modelValue.main_apps, 'onUpdate:modelValue': v => update('main_apps', v), type: 'textarea', rows: 3 })
      ]),
      h('el-form-item', { label: '安全设备清单' }, [
        h('el-input', { modelValue: props.modelValue.security_devices, 'onUpdate:modelValue': v => update('security_devices', v), type: 'textarea', rows: 3, placeholder: '如：防火墙 ×2、IDS ×1、WAF ×1' })
      ])
    ])
  }
}

const FormTest = {
  props: ['modelValue'],
  setup(props, { emit }) {
    const update = (k, v) => emit('update:modelValue', { ...props.modelValue, [k]: v })
    return () => h('div', { class: 'step-form' }, [
      h('el-form-item', { label: '测评机构' }, [
        h('el-input', { modelValue: props.modelValue.test_org, 'onUpdate:modelValue': v => update('test_org', v) })
      ]),
      h('el-form-item', { label: '测评时间' }, [
        h('el-date-picker', { modelValue: props.modelValue.test_time, 'onUpdate:modelValue': v => update('test_time', v), type: 'date', valueFormat: 'YYYY-MM-DD', style: 'width:100%' })
      ]),
      h('el-form-item', { label: '测评结论' }, [
        h('el-select', {
          modelValue: props.modelValue.test_conclusion,
          'onUpdate:modelValue': v => update('test_conclusion', v),
          placeholder: '请选择', style: 'width:100%'
        }, () => ['优', '良', '中', '差'].map(n => h('el-option', { key: n, label: n, value: n })))
      ])
    ])
  }
}

const formComponents = [FormSystem, FormUnit, FormPersons, FormSysinfo, FormTest]
const currentFormComponent = computed(() => formComponents[currentStep.value])

function prev() { if (currentStep.value > 0) currentStep.value-- }
function next() {
  if (currentStep.value < steps.length - 1) currentStep.value++
}

async function onSave() {
  await portalApi.filingSubmit({ ...filing.value, filing_status: '暂存' })
  ElMessage.success('已暂存')
}

async function onFinalSubmit() {
  try {
    await ElMessageBox.confirm('提交后备案信息将不可修改，如需修改请联系管理员解锁。确认提交？', '提交确认', { type: 'warning' })
    submitting.value = true
    await portalApi.filingSubmit({ ...filing.value, filing_status: '已提交' })
    filing.value.filing_status = '已提交'
    ElMessage.success('备案已提交，可在列表中下载生成的备案 PDF')
  } finally {
    submitting.value = false
  }
}

onMounted(() => {
  // 预填演示数据
  Object.assign(filing.value, {
    unit_name: '华兴证券股份有限公司',
    credit_code: '91440300100008888K',
    unit_nature: '企业',
    industry: '金融',
    address: '深圳市福田区福华三路88号',
    contact_person: '陈志远',
    contact_phone: '13812346789'
  })
})
</script>

<style lang="scss" scoped>
.filing-page {
  padding: 28px;
  display: flex;
  justify-content: center;
}
.wizard-card {
  width: 100%;
  max-width: 820px;
  background: var(--card);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-lg);
  padding: 32px 40px;
}
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 24px;
  h2 { margin: 0; font-size: 20px; font-weight: 700; }
}
.step-wizard {
  display: flex;
  align-items: center;
  margin-bottom: 16px;
}
.step-item {
  flex: 1;
  display: flex;
  align-items: center;
  position: relative;
}
.step-dot {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  background: #E4ECFC;
  color: var(--text-placeholder);
  z-index: 2;
  &.active { background: var(--primary); color: #fff; }
  &.done { background: var(--accent); color: #fff; }
}
.step-title {
  font-size: 12px;
  margin-left: 8px;
  color: var(--text-muted);
  &.active { color: var(--primary); font-weight: 700; }
}
.step-line {
  flex: 1;
  height: 2px;
  margin: 0 12px;
  background: #E4ECFC;
  &.active { background: var(--accent); }
}
.step-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 32px;
  padding-top: 20px;
  border-top: 1px solid var(--card-border);
}
.step-form {
  :deep(.el-form-item) { margin-bottom: 18px; }
}
</style>

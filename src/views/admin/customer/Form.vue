<template>
  <AdminLayout>
    <template #header-actions>
      <el-button @click="$router.back()">取消</el-button>
      <el-button type="primary" :loading="saving" @click="onSubmit">保存</el-button>
    </template>

    <div class="form-card">
      <h3 class="form-title">基础信息</h3>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="140px" label-position="right">
        <el-row :gutter="20">
          <el-col :span="12">
            <el-form-item label="客户名称" prop="customer_name">
              <el-input v-model="form.customer_name" placeholder="请输入客户名称" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="客户类型" prop="customer_type">
              <el-select v-model="form.customer_type" placeholder="请选择" style="width:100%">
                <el-option v-for="t in ENUMS.customer_type" :key="t.value" :label="t.label" :value="t.value" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="统一社会信用代码" prop="credit_code">
              <el-input v-model="form.credit_code" placeholder="18 位字母数字组合" maxlength="18" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="所属行业" prop="industry">
              <el-select v-model="form.industry" placeholder="请选择" style="width:100%">
                <el-option v-for="i in ENUMS.industry" :key="i" :label="i" :value="i" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="企业规模">
              <el-select v-model="form.enterprise_scale" placeholder="请选择（可选）" clearable style="width:100%">
                <el-option v-for="s in ENUMS.enterprise_scale" :key="s" :label="s" :value="s" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="测评需求类型" prop="test_demand_type">
              <el-select v-model="form.test_demand_type" multiple placeholder="可多选" style="width:100%">
                <el-option v-for="t in ENUMS.test_demand_type || ['等保测评','风险评估','渗透测试','代码审计','安全咨询','其他']" :key="t" :label="t" :value="t" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="24">
            <el-form-item label="注册地址" prop="reg_address">
              <el-input v-model="form.reg_address" placeholder="请输入注册地址" />
            </el-form-item>
          </el-col>
          <el-col :span="24">
            <el-form-item label="办公地址">
              <el-input v-model="form.office_address" placeholder="可选" />
            </el-form-item>
          </el-col>
          <el-col :span="24">
            <el-form-item label="主营业务">
              <el-input v-model="form.main_business" type="textarea" :rows="3" placeholder="请简要描述" />
            </el-form-item>
          </el-col>
          <el-col :span="24">
            <el-form-item label="特殊要求">
              <el-input v-model="form.special_requirement" type="textarea" :rows="2" placeholder="报告格式、保密协议等" />
            </el-form-item>
          </el-col>
        </el-row>
      </el-form>

      <div class="action-bar">
        <el-button @click="onCheckDup">查重</el-button>
        <el-button type="primary" :loading="saving" @click="onSubmit">保存</el-button>
      </div>
    </div>

    <!-- 查重结果 -->
    <el-dialog v-model="dupVisible" title="查重结果" width="600">
      <el-empty v-if="dupResults.length === 0" description="未发现重复客户" />
      <div v-else>
        <el-alert type="warning" :closable="false" show-icon style="margin-bottom: 12px">
          检测到 {{ dupResults.length }} 条可能的重复记录，请确认
        </el-alert>
        <div v-for="r in dupResults" :key="r.customer_id" class="dup-row">
          <div>
            <div style="font-weight:600">{{ r.customer_name }}</div>
            <div style="font-size:12px;color:var(--text-muted)">{{ r.customer_id }} · 匹配类型：{{ r.match_type === 'credit_code_exact' ? '信用代码精确' : '名称模糊' }}</div>
          </div>
          <el-button size="small" @click="$router.push(`/admin/customers/${r.customer_id}`)">查看</el-button>
        </div>
      </div>
    </el-dialog>
  </AdminLayout>
</template>

<script setup>
import { ref, reactive, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import AdminLayout from '@/layout/AdminLayout.vue'
import { customerApi } from '@/api'
import { validateCreditCode } from '@/utils/format'

const router = useRouter()
const route = useRoute()
const isEdit = computed(() => !!route.params.id)
const formRef = ref()
const saving = ref(false)
const dupVisible = ref(false)
const dupResults = ref([])

const ENUMS = {
  customer_type: [{ value: '业主', label: '业主' }, { value: '三方', label: '三方' }, { value: '其他', label: '其他' }, { value: '政府', label: '政府' }, { value: '事业单位', label: '事业单位' }, { value: '个人', label: '个人' }],
  industry: ['金融', '政府', '能源', '制造', '电信', '医疗', '教育', '交通', '互联网', '其他'],
  enterprise_scale: ['大型', '中型', '小型', '微型'],
  test_demand_type: ['等保测评', '风险评估', '渗透测试', '代码审计', '安全咨询', '其他']
}

const form = reactive({
  customer_name: '',
  customer_type: '',
  credit_code: '',
  industry: '',
  enterprise_scale: '',
  reg_address: '',
  office_address: '',
  main_business: '',
  special_requirement: '',
  test_demand_type: []
})

const rules = {
  customer_name: [{ required: true, message: '请输入客户名称', trigger: 'blur' }],
  customer_type: [{ required: true, message: '请选择客户类型', trigger: 'change' }],
  credit_code: [
    { required: true, message: '请输入统一社会信用代码', trigger: 'blur' },
    {
      validator: (_, v, cb) => {
        if (!v) return cb()
        if (!validateCreditCode(v)) return cb(new Error('信用代码格式不合法（GB 32100）'))
        cb()
      },
      trigger: 'blur'
    }
  ],
  industry: [{ required: true, message: '请选择行业', trigger: 'change' }],
  reg_address: [{ required: true, message: '请输入注册地址', trigger: 'blur' }]
}

async function loadData() {
  if (!isEdit.value) return
  const res = await customerApi.detail(route.params.id)
  Object.assign(form, res.data.basic)
  form.main_business = res.data.biz_info?.main_business || ''
  form.test_demand_type = res.data.biz_info?.test_demand_type || []
}

async function onCheckDup() {
  if (!form.customer_name && !form.credit_code) {
    ElMessage.warning('请先填写客户名称或信用代码')
    return
  }
  const res = await customerApi.checkDuplicate({
    customer_name: form.customer_name,
    credit_code: form.credit_code
  })
  dupResults.value = res.data.duplicates
  dupVisible.value = true
}

async function onSubmit() {
  if (!formRef.value) return
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    saving.value = true
    try {
      if (isEdit.value) {
        await customerApi.update(route.params.id, form)
        ElMessage.success('更新成功')
      } else {
        const res = await customerApi.create({
          ...form,
          biz_info: {
            main_business: form.main_business,
            test_demand_type: form.test_demand_type,
            special_requirement: form.special_requirement
          }
        })
        ElMessage.success(`创建成功：${res.data.customer_id}`)
      }
      router.push('/admin/customers')
    } finally {
      saving.value = false
    }
  })
}

import { onMounted } from 'vue'
onMounted(loadData)
</script>

<style lang="scss" scoped>
.form-card {
  background: var(--card);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-lg);
  padding: 28px;
  max-width: 960px;
}
.form-title {
  font-size: 16px;
  font-weight: 700;
  margin: 0 0 20px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--card-border);
}
.action-bar {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 24px;
  padding-top: 20px;
  border-top: 1px solid var(--card-border);
}
.dup-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px;
  background: var(--bg);
  border-radius: var(--radius-sm);
  margin-bottom: 8px;
}
</style>

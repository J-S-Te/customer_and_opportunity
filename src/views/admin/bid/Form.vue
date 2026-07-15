<template>
  <AdminLayout>
    <template #header-actions>
      <el-button @click="$router.back()">取消</el-button>
      <el-button type="primary" :loading="saving" @click="onSubmit">保存</el-button>
    </template>

    <div class="form-card">
      <h3 class="form-title">基本信息</h3>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
        <el-row :gutter="20">
          <el-col :span="12">
            <el-form-item label="关联商机" prop="opportunity_id">
              <el-select v-model="form.opportunity_id" filterable placeholder="请选择" style="width:100%">
                <el-option v-for="o in opps" :key="o.opportunity_id" :label="`${o.opp_name}`" :value="o.opportunity_id" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="项目名称" prop="project_name">
              <el-input v-model="form.project_name" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="招标编号" prop="bid_code">
              <el-input v-model="form.bid_code" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="代理机构" prop="agency_name">
              <el-input v-model="form.agency_name" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="招标人" prop="tender_name">
              <el-input v-model="form.tender_name" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="投标截止" prop="bid_deadline">
              <el-date-picker v-model="form.bid_deadline" type="datetime" value-format="YYYY-MM-DD HH:mm:ss" style="width:100%" />
            </el-form-item>
          </el-col>
        </el-row>
      </el-form>
    </div>

    <div class="form-card">
      <h3 class="form-title">保证金</h3>
      <el-form label-width="120px">
        <el-row :gutter="20">
          <el-col :span="8">
            <el-form-item label="金额">
              <el-input-number v-model="form.deposit.amount" :min="0" :precision="2" :step="1000" style="width:100%" />
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item label="预计到期">
              <el-date-picker v-model="form.deposit.expected_refund_date" type="date" value-format="YYYY-MM-DD" style="width:100%" />
            </el-form-item>
          </el-col>
          <el-col :span="8">
            <el-form-item label="缴纳时间">
              <el-date-picker v-model="form.deposit.pay_time" type="datetime" value-format="YYYY-MM-DD HH:mm:ss" style="width:100%" />
            </el-form-item>
          </el-col>
        </el-row>
        <el-alert type="info" :closable="false" show-icon>
          预计到期时间由用户自定义。系统将在到期前 7 天自动提醒。
        </el-alert>
      </el-form>
    </div>
  </AdminLayout>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import AdminLayout from '@/layout/AdminLayout.vue'
import { bidApi, opportunityApi } from '@/api'

const router = useRouter()
const formRef = ref()
const saving = ref(false)
const opps = ref([])

const form = reactive({
  opportunity_id: '',
  project_name: '',
  bid_code: '',
  tender_name: '',
  agency_name: '',
  bid_deadline: '',
  deposit: { amount: 0, expected_refund_date: '', pay_time: '' }
})

const rules = {
  opportunity_id: [{ required: true, message: '请选择关联商机', trigger: 'change' }],
  project_name: [{ required: true, message: '请输入项目名称', trigger: 'blur' }],
  bid_code: [{ required: true, message: '请输入招标编号', trigger: 'blur' }],
  tender_name: [{ required: true, message: '请输入招标人', trigger: 'blur' }],
  bid_deadline: [{ required: true, message: '请选择投标截止时间', trigger: 'change' }]
}

async function loadOpps() {
  const res = await opportunityApi.list({ page: 1, page_size: 100 })
  opps.value = res.data.list
}

async function onSubmit() {
  if (!formRef.value) return
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    saving.value = true
    try {
      const res = await bidApi.create({
        ...form,
        bid_deadline: form.bid_deadline,
        deposit: {
          amount: form.deposit.amount,
          expected_refund_date: form.deposit.expected_refund_date,
          pay_time: form.deposit.pay_time
        }
      })
      ElMessage.success(`投标项目创建成功：${res.data.bid_id}`)
      router.push(`/admin/bids/${res.data.bid_id}`)
    } finally {
      saving.value = false
    }
  })
}

onMounted(loadOpps)
</script>

<style lang="scss" scoped>
.form-card {
  background: var(--card);
  border: 1px solid var(--card-border);
  border-radius: var(--radius-lg);
  padding: 24px 28px;
  margin-bottom: 16px;
}
.form-title {
  font-size: 16px;
  font-weight: 700;
  margin: 0 0 20px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--card-border);
}
</style>

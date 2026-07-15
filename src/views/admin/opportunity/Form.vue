<template>
  <AdminLayout>
    <template #header-actions>
      <el-button @click="$router.back()">取消</el-button>
      <el-button type="primary" :loading="saving" @click="onSubmit">保存</el-button>
    </template>

    <div class="form-card">
      <h3 class="form-title">基础信息</h3>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
        <el-row :gutter="20">
          <el-col :span="12">
            <el-form-item label="关联客户" prop="customer_id">
              <el-select v-model="form.customer_id" filterable placeholder="请选择客户" style="width:100%">
                <el-option v-for="c in customers" :key="c.customer_id" :label="`${c.customer_name} (${c.customer_id})`" :value="c.customer_id" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="商机名称" prop="opp_name">
              <el-input v-model="form.opp_name" placeholder="请输入商机名称" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="商机来源" prop="opp_source">
              <el-select v-model="form.opp_source" placeholder="请选择" style="width:100%">
                <el-option v-for="s in sources" :key="s" :label="s" :value="s" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="商机类型" prop="opp_type">
              <el-select v-model="form.opp_type" placeholder="请选择" style="width:100%">
                <el-option v-for="t in types" :key="t" :label="t" :value="t" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="预计金额" prop="expected_amount">
              <el-input-number v-model="form.expected_amount" :min="0" :precision="2" :step="1000" style="width:100%" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="预计签单时间" prop="expected_sign_date">
              <el-date-picker v-model="form.expected_sign_date" type="date" value-format="YYYY-MM-DD" style="width:100%" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="销售负责人" prop="sales_owner">
              <el-input v-model="form.sales_owner" placeholder="工号或姓名" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="支持人员">
              <el-input v-model="form.support_team_text" placeholder="多个用逗号分隔" />
            </el-form-item>
          </el-col>
        </el-row>
      </el-form>
    </div>

    <div class="form-card">
      <h3 class="form-title">竞争信息</h3>
      <el-table :data="form.competitor_info" border>
        <el-table-column label="对手名称">
          <template #default="{ row }">
            <el-input v-model="row.name" placeholder="竞争对手名称" />
          </template>
        </el-table-column>
        <el-table-column label="优势">
          <template #default="{ row }"><el-input v-model="row.advantage" /></template>
        </el-table-column>
        <el-table-column label="劣势">
          <template #default="{ row }"><el-input v-model="row.disadvantage" /></template>
        </el-table-column>
        <el-table-column label="我方策略">
          <template #default="{ row }"><el-input v-model="row.our_strategy" /></template>
        </el-table-column>
        <el-table-column width="80" label="操作">
          <template #default="{ $index }">
            <el-button link type="danger" @click="form.competitor_info.splice($index, 1)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-button style="margin-top:10px" @click="form.competitor_info.push({ name:'', advantage:'', disadvantage:'', our_strategy:'' })">
        <el-icon><Plus /></el-icon>添加竞争对手
      </el-button>
    </div>
  </AdminLayout>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import AdminLayout from '@/layout/AdminLayout.vue'
import { customerApi, opportunityApi } from '@/api'

const router = useRouter()
const formRef = ref()
const saving = ref(false)
const customers = ref([])
const sources = ['老客户推荐', '公开招标', '自主开发', '客户介绍', '行业活动', '其他']
const types = ['等保测评', '风险评估', '渗透测试', '代码审计', '安全咨询', '综合']

const form = reactive({
  customer_id: '',
  opp_name: '',
  opp_source: '',
  opp_type: '',
  expected_amount: 0,
  expected_sign_date: '',
  sales_owner: '',
  support_team_text: '',
  competitor_info: []
})

const rules = {
  customer_id: [{ required: true, message: '请选择关联客户', trigger: 'change' }],
  opp_name: [{ required: true, message: '请输入商机名称', trigger: 'blur' }],
  opp_source: [{ required: true, message: '请选择商机来源', trigger: 'change' }],
  opp_type: [{ required: true, message: '请选择商机类型', trigger: 'change' }],
  expected_amount: [{ required: true, validator: (_, v, cb) => v > 0 ? cb() : cb(new Error('金额必须大于 0')), trigger: 'blur' }],
  expected_sign_date: [{ required: true, message: '请选择预计签单时间', trigger: 'change' }],
  sales_owner: [{ required: true, message: '请输入销售负责人', trigger: 'blur' }]
}

async function loadCustomers() {
  const res = await customerApi.list({ page: 1, page_size: 100 })
  customers.value = res.data.list
}

async function onSubmit() {
  if (!formRef.value) return
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    saving.value = true
    try {
      const res = await opportunityApi.create({
        customer_id: form.customer_id,
        opp_name: form.opp_name,
        opp_source: form.opp_source,
        opp_type: form.opp_type,
        expected_amount: form.expected_amount,
        expected_sign_date: form.expected_sign_date,
        sales_owner: form.sales_owner,
        support_team: form.support_team_text.split(/[,，]/).filter(Boolean),
        competitor_info: form.competitor_info.filter(c => c.name)
      })
      ElMessage.success(`商机创建成功：${res.data.opportunity_id}`)
      router.push(`/admin/opportunities/${res.data.opportunity_id}`)
    } finally {
      saving.value = false
    }
  })
}

onMounted(loadCustomers)
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

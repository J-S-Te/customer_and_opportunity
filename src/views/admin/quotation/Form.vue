<template>
  <AdminLayout>
    <template #header-actions>
      <el-button @click="$router.back()">取消</el-button>
      <el-button type="primary" :loading="saving" @click="onSubmit">保存</el-button>
    </template>

    <!-- 关联商机 -->
    <div class="form-card">
      <h3 class="form-title">基本信息</h3>
      <el-form :model="form" label-width="120px">
        <el-row :gutter="20">
          <el-col :span="12">
            <el-form-item label="关联商机" required>
              <el-select v-model="form.opportunity_id" filterable placeholder="请选择商机" style="width:100%" @change="onOppChange">
                <el-option v-for="o in opps" :key="o.opportunity_id" :label="`${o.opp_name} (${o.opportunity_id})`" :value="o.opportunity_id" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="客户名称">
              <el-input :value="form.customer_name" disabled />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="服务周期">
              <el-input v-model="form.service_period" placeholder="如：自合同签订之日起 90 个工作日" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="质保条款">
              <el-input v-model="form.warranty_terms" placeholder="如：质保期 1 年" />
            </el-form-item>
          </el-col>
        </el-row>
      </el-form>
    </div>

    <!-- 报价明细 -->
    <div class="form-card">
      <h3 class="form-title">报价明细</h3>
      <el-table :data="form.items" border>
        <el-table-column label="服务项目" min-width="180">
          <template #default="{ row }"><el-input v-model="row.service_item" /></template>
        </el-table-column>
        <el-table-column label="服务内容" min-width="220">
          <template #default="{ row }"><el-input v-model="row.service_content" /></template>
        </el-table-column>
        <el-table-column label="数量" width="100">
          <template #default="{ row }">
            <el-input-number v-model="row.quantity" :min="1" :precision="0" size="small" />
          </template>
        </el-table-column>
        <el-table-column label="单价" width="140">
          <template #default="{ row }">
            <el-input-number v-model="row.unit_price" :min="0" :precision="2" :step="1000" size="small" style="width:100%" />
          </template>
        </el-table-column>
        <el-table-column label="折扣" width="120">
          <template #default="{ row }">
            <el-input-number v-model="row.discount_rate" :min="0" :max="1" :step="0.05" :precision="2" size="small" style="width:100%" />
          </template>
        </el-table-column>
        <el-table-column label="金额" width="120">
          <template #default="{ row }">
            <span style="font-family:monospace;font-weight:600">¥{{ calcLineAmount(row).toLocaleString() }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ $index }">
            <el-button link type="danger" @click="form.items.splice($index, 1)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-button style="margin-top:10px" @click="addItem"><el-icon><Plus /></el-icon>添加明细</el-button>

      <div class="totals">
        <div class="total-row"><span>小计：</span><strong>¥{{ subtotal.toLocaleString() }}</strong></div>
        <div class="total-row"><span>折扣金额：</span><strong style="color:var(--accent)">-¥{{ discountAmount.toLocaleString() }}</strong></div>
        <div class="total-row"><span>税费 (6%)：</span><strong>¥{{ taxAmount.toLocaleString() }}</strong></div>
        <div class="total-row total-final"><span>总金额：</span><strong style="color:var(--primary);font-size:18px">¥{{ totalAmount.toLocaleString() }}</strong></div>
      </div>
    </div>

    <!-- 付款条款 -->
    <div class="form-card">
      <h3 class="form-title">付款条款</h3>
      <el-radio-group v-model="form.payment_terms_default" style="margin-bottom: 12px">
        <el-radio-button :value="true">使用系统默认（10 工作日 50% + 验收 50%）</el-radio-button>
        <el-radio-button :value="false">自定义条款</el-radio-button>
      </el-radio-group>
      <el-table v-if="!form.payment_terms_default" :data="form.payment_terms" border>
        <el-table-column label="阶段名称">
          <template #default="{ row }"><el-input v-model="row.phase_name" /></template>
        </el-table-column>
        <el-table-column label="付款比例" width="140">
          <template #default="{ row }">
            <el-input-number v-model="row.pay_ratio" :min="0" :max="1" :step="0.1" :precision="2" size="small" style="width:100%" />
          </template>
        </el-table-column>
        <el-table-column label="触发条件说明">
          <template #default="{ row }"><el-input v-model="row.condition_desc" /></template>
        </el-table-column>
        <el-table-column label="排序" width="80">
          <template #default="{ row }"><el-input-number v-model="row.sort_order" :min="1" size="small" /></template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ $index }">
            <el-button link type="danger" @click="form.payment_terms.splice($index, 1)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="!form.payment_terms_default" style="margin-top:10px">
        <el-button @click="form.payment_terms.push({ phase_name:'', pay_ratio:0, condition_desc:'', sort_order: form.payment_terms.length + 1 })">+ 添加条款</el-button>
        <span style="margin-left:12px;color:var(--warning);font-size:12px">⚠️ 多段付款比例合计必须 = 1.0</span>
      </div>
    </div>

    <div class="form-card">
      <h3 class="form-title">商务条款</h3>
      <el-form label-width="120px">
        <el-form-item label="特殊约定">
          <el-input v-model="form.special_terms" type="textarea" :rows="3" />
        </el-form-item>
      </el-form>
    </div>
  </AdminLayout>
</template>

<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import AdminLayout from '@/layout/AdminLayout.vue'
import { quotationApi, opportunityApi } from '@/api'

const router = useRouter()
const saving = ref(false)
const opps = ref([])

const form = reactive({
  opportunity_id: '',
  customer_name: '',
  service_period: '',
  warranty_terms: '',
  payment_terms_default: true,
  payment_terms: [
    { phase_name: '合同签定后 10 工作日', pay_ratio: 0.5, condition_desc: '合同签订后 10 个工作日内', sort_order: 1 },
    { phase_name: '验收后', pay_ratio: 0.5, condition_desc: '项目验收合格后', sort_order: 2 }
  ],
  items: [
    { service_item: '等级保护测评', service_content: '三级等保测评（含整改咨询）', quantity: 1, unit_price: 400000, discount_rate: 0.95 }
  ],
  special_terms: ''
})

function calcLineAmount(it) {
  return (it.quantity || 0) * (it.unit_price || 0) * (it.discount_rate || 1)
}
const subtotal = computed(() => form.items.reduce((s, it) => s + (it.quantity * it.unit_price), 0))
const discountAmount = computed(() => form.items.reduce((s, it) => s + (it.quantity * it.unit_price * (1 - (it.discount_rate || 1))), 0))
const afterDiscount = computed(() => subtotal.value - discountAmount.value)
const taxAmount = computed(() => afterDiscount.value * 0.06)
const totalAmount = computed(() => afterDiscount.value + taxAmount.value)

function addItem() {
  form.items.push({ service_item: '', service_content: '', quantity: 1, unit_price: 0, discount_rate: 1 })
}

function onOppChange(id) {
  const o = opps.value.find(x => x.opportunity_id === id)
  if (o) form.customer_name = o.customer_name
}

async function loadOpps() {
  const res = await opportunityApi.list({ page: 1, page_size: 100 })
  opps.value = res.data.list
}

async function onSubmit() {
  if (!form.opportunity_id) {
    ElMessage.warning('请选择关联商机')
    return
  }
  if (!form.items.length) {
    ElMessage.warning('请添加至少一条报价明细')
    return
  }
  if (!form.payment_terms_default) {
    const total = form.payment_terms.reduce((s, p) => s + (p.pay_ratio || 0), 0)
    if (Math.abs(total - 1) > 0.001) {
      ElMessage.warning(`付款比例合计必须等于 1.0，当前为 ${total.toFixed(2)}`)
      return
    }
  }
  saving.value = true
  try {
    const res = await quotationApi.create({
      opportunity_id: form.opportunity_id,
      items: form.items,
      payment_terms_default: form.payment_terms_default,
      payment_terms: form.payment_terms_default ? [] : form.payment_terms,
      service_period: form.service_period,
      warranty_terms: form.warranty_terms,
      special_terms: form.special_terms
    })
    ElMessage.success(`报价单创建成功：${res.data.quotation_id}`)
    router.push(`/admin/quotations/${res.data.quotation_id}`)
  } finally {
    saving.value = false
  }
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
.totals {
  margin-top: 16px;
  padding: 16px;
  background: var(--bg);
  border-radius: var(--radius);
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 8px;
}
.total-row {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 320px;
  justify-content: space-between;
  font-size: 14px;
}
.total-final {
  padding-top: 10px;
  border-top: 1px solid var(--card-border);
  font-size: 16px;
}
</style>

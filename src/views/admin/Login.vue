<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-logo">A</div>
      <h2>客户与商机管理子系统</h2>
      <p class="login-sub">内部销售管理 · 工作台</p>
      <el-form ref="formRef" :model="form" :rules="rules" class="login-form" size="large" @keyup.enter="onSubmit">
        <el-form-item prop="username">
          <el-input v-model="form.username" placeholder="请输入工号 / 邮箱" :prefix-icon="User" />
        </el-form-item>
        <el-form-item prop="password">
          <el-input v-model="form.password" type="password" show-password placeholder="请输入密码" :prefix-icon="Lock" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="loading" class="login-submit" @click="onSubmit">登 录</el-button>
        </el-form-item>
      </el-form>
      <div class="login-tips">
        <span>测试环境：任意用户名 + 任意密码（mock 模式）</span>
      </div>
      <el-divider />
      <div class="login-extra">
        <span>客户入口？</span>
        <router-link to="/portal/login" class="crm-text-link">前往客户门户登录</router-link>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { User, Lock } from '@element-plus/icons-vue'
import { useUserStore } from '@/store/user'

const router = useRouter()
const route = useRoute()
const userStore = useUserStore()
const formRef = ref()
const loading = ref(false)
const form = reactive({ username: '', password: '' })
const rules = {
  username: [{ required: true, message: '请输入工号 / 邮箱', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }]
}

async function onSubmit() {
  if (!formRef.value) return
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    loading.value = true
    try {
      // mock 登录：实际联调改为 POST /admin/login
      const user = { user_id: 'U001', name: form.username || '销售经理', role: 'sales_manager', avatar: '' }
      userStore.setAdminAuth('mock-admin-token-' + Date.now(), user)
      ElMessage.success('登录成功')
      const redirect = route.query.redirect || '/admin/customers'
      router.replace(redirect)
    } finally {
      loading.value = false
    }
  })
}
</script>

<style lang="scss" scoped>
.login-page {
  height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #EFF6FF 0%, #F0F9FF 100%);
}
.login-card {
  width: 420px;
  background: var(--card);
  border-radius: var(--radius-xl);
  padding: 40px;
  box-shadow: var(--shadow-lg);
  border: 1px solid var(--card-border);
  text-align: center;
}
.login-logo {
  width: 56px; height: 56px;
  background: var(--primary);
  border-radius: var(--radius-lg);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-weight: 700;
  font-size: 24px;
  margin-bottom: 16px;
}
.login-card h2 { margin: 0 0 4px; font-size: 22px; font-weight: 700; }
.login-sub { color: var(--text-muted); font-size: 13px; margin-bottom: 28px; }
.login-form { text-align: left; }
.login-submit { width: 100%; height: 44px; font-size: 15px; font-weight: 600; }
.login-tips { margin-top: 16px; font-size: 12px; color: var(--text-muted); }
.login-extra { font-size: 13px; color: var(--text-secondary); }
</style>

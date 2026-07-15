<template>
  <div class="portal-login-page">
    <div class="login-bg" />
    <div class="login-card">
      <div class="brand">
        <div class="brand-logo">A</div>
        <h1>客户自助服务平台</h1>
        <p>测评业务全生命周期管理 · 客户专属门户</p>
      </div>
      <el-form ref="formRef" :model="form" :rules="rules" size="large">
        <el-form-item prop="login_name">
          <el-input v-model="form.login_name" placeholder="企业账号 / 手机号" :prefix-icon="User" />
        </el-form-item>
        <el-form-item prop="password">
          <el-input v-model="form.password" type="password" show-password placeholder="登录密码" :prefix-icon="Lock" />
        </el-form-item>
        <el-form-item prop="verify_code" v-if="needVerify">
          <el-input v-model="form.verify_code" placeholder="请输入验证码" :prefix-icon="Key">
            <template #append>
              <el-button @click="sendCode" :disabled="countdown > 0">
                {{ countdown > 0 ? `${countdown}s 后重发` : '发送验证码' }}
              </el-button>
            </template>
          </el-input>
        </el-form-item>
        <el-button type="primary" :loading="loading" class="login-btn" @click="onSubmit">登 录</el-button>
      </el-form>
      <el-alert type="info" :closable="false" show-icon style="margin-top:16px">
        <template #title>双因素认证已启用</template>
        本期仅支持站内验证码，短信通道预留
      </el-alert>
      <div class="login-footer">
        <a>忘记密码</a><span>|</span><a>注册新账号</a>
      </div>
      <div class="back-admin">
        <router-link to="/admin/login">← 返回管理端登录</router-link>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { User, Lock, Key } from '@element-plus/icons-vue'
import { useUserStore } from '@/store/user'

const router = useRouter()
const route = useRoute()
const userStore = useUserStore()
const formRef = ref()
const loading = ref(false)
const needVerify = ref(true)
const countdown = ref(0)

const form = reactive({
  login_name: 'huaxing',
  password: 'demo123',
  verify_code: ''
})

const rules = {
  login_name: [{ required: true, message: '请输入账号', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }]
}

function sendCode() {
  ElMessage.success('验证码已发送（mock：任意 6 位）')
  countdown.value = 60
  const timer = setInterval(() => {
    countdown.value--
    if (countdown.value <= 0) clearInterval(timer)
  }, 1000)
}

async function onSubmit() {
  if (!formRef.value) return
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    loading.value = true
    try {
      // mock 登录
      const account = {
        account_id: 'PA001',
        customer_id: 'KH202606150001',
        customer_name: '华兴证券股份有限公司',
        contact_phone: '13812346789'
      }
      userStore.setPortalAuth('mock-portal-token-' + Date.now(), account)
      ElMessage.success('登录成功')
      const redirect = route.query.redirect || '/portal/dashboard'
      router.replace(redirect)
    } finally {
      loading.value = false
    }
  })
}
</script>

<style lang="scss" scoped>
.portal-login-page {
  height: 100vh;
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
}
.login-bg {
  position: absolute;
  inset: 0;
  background: linear-gradient(135deg, #1E3A8A 0%, #2563EB 50%, #3B82F6 100%);
  z-index: 0;
  &::before {
    content: '';
    position: absolute;
    width: 600px;
    height: 600px;
    background: radial-gradient(circle, rgba(255,255,255,0.1) 0%, transparent 70%);
    top: -200px;
    right: -200px;
  }
}
.login-card {
  position: relative;
  z-index: 1;
  width: 440px;
  background: var(--card);
  border-radius: var(--radius-xl);
  padding: 40px;
  box-shadow: 0 25px 80px rgba(0,0,0,0.2);
}
.brand { text-align: center; margin-bottom: 28px; }
.brand-logo {
  width: 56px;
  height: 56px;
  background: linear-gradient(135deg, #2563EB, #3B82F6);
  border-radius: var(--radius-lg);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-weight: 700;
  font-size: 24px;
  margin-bottom: 12px;
}
.brand h1 { font-size: 22px; font-weight: 700; margin: 0 0 6px; }
.brand p { color: var(--text-muted); font-size: 13px; margin: 0; }
.login-btn { width: 100%; height: 44px; font-size: 15px; font-weight: 600; }
.login-footer {
  text-align: center;
  margin-top: 20px;
  font-size: 13px;
  color: var(--text-muted);
  a { margin: 0 8px; cursor: pointer; }
  span { color: var(--card-border); }
}
.back-admin {
  text-align: center;
  margin-top: 16px;
  font-size: 12px;
}
</style>

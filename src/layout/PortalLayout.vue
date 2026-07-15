<template>
  <div class="portal-layout">
    <!-- 顶部导航 -->
    <nav class="portal-nav">
      <div class="portal-nav-logo" @click="$router.push('/portal/dashboard')">
        <div class="logo-icon">A</div>
        <span class="logo-text">客户服务门户</span>
      </div>
      <div class="portal-nav-links">
        <router-link
          v-for="item in navItems"
          :key="item.path"
          :to="item.path"
          class="nav-link"
          :class="{ active: $route.path.startsWith(item.path) }"
        >
          {{ item.label }}
        </router-link>
      </div>
      <div class="portal-nav-right">
        <el-tooltip content="站内消息">
          <el-badge :value="3" class="nav-icon-btn">
            <el-icon><Bell /></el-icon>
          </el-badge>
        </el-tooltip>
        <div class="user-info" @click="$router.push('/portal/dashboard')">
          <div class="user-avatar">{{ userInitial }}</div>
          <span class="user-name">{{ customerName }}</span>
        </div>
        <el-dropdown @command="onCommand">
          <el-icon class="more-icon"><ArrowDown /></el-icon>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="logout">退出登录</el-dropdown-item>
              <el-dropdown-item command="settings">账号设置</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </nav>

    <!-- 内容区 -->
    <div class="portal-content">
      <slot />
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useUserStore } from '@/store/user'
import { Bell, ArrowDown } from '@element-plus/icons-vue'

const router = useRouter()
const userStore = useUserStore()

const navItems = [
  { path: '/portal/dashboard', label: '工作台' },
  { path: '/portal/projects', label: '我的项目' },
  { path: '/portal/reports', label: '报告中心' },
  { path: '/portal/filing', label: '备案信息' },
  { path: '/portal/evaluation', label: '服务评价' },
  { path: '/portal/feedback', label: '反馈与投诉' }
]

const customerName = computed(() => {
  return userStore.portalUser?.customer_name || '客户'
})

const userInitial = computed(() => {
  return customerName.value[0] || '客'
})

function onCommand(cmd) {
  if (cmd === 'logout') {
    userStore.clearPortalAuth()
    router.push('/portal/login')
  }
}
</script>

<style lang="scss" scoped>
.portal-layout {
  display: flex;
  flex-direction: column;
  height: 100vh;
  background: var(--bg);
}

.portal-nav {
  height: 60px;
  min-height: 60px;
  background: var(--card);
  display: flex;
  align-items: center;
  padding: 0 32px;
  gap: 40px;
  box-shadow: var(--shadow-sm);
  z-index: 10;

  .portal-nav-logo {
    display: flex;
    align-items: center;
    gap: 10px;
    cursor: pointer;
    .logo-icon {
      width: 32px;
      height: 32px;
      background: var(--primary);
      border-radius: 8px;
      display: flex;
      align-items: center;
      justify-content: center;
      color: #fff;
      font-weight: 700;
      font-size: 16px;
    }
    .logo-text {
      font-size: 16px;
      font-weight: 700;
    }
  }

  .portal-nav-links {
    display: flex;
    gap: 28px;
    flex: 1;
  }
  .nav-link {
    color: var(--text-secondary);
    font-size: 14px;
    font-weight: 500;
    padding: 8px 0;
    border-bottom: 2px solid transparent;
    transition: all 0.15s;
    &:hover {
      color: var(--primary);
      text-decoration: none;
    }
    &.active {
      color: var(--primary);
      border-bottom-color: var(--primary);
      font-weight: 600;
    }
  }

  .portal-nav-right {
    display: flex;
    align-items: center;
    gap: 16px;
  }
  .nav-icon-btn {
    cursor: pointer;
    font-size: 18px;
    color: var(--text-secondary);
  }
  .user-info {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    padding: 4px 8px;
    border-radius: 8px;
    &:hover { background: var(--bg); }
    .user-avatar {
      width: 32px;
      height: 32px;
      background: var(--primary);
      border-radius: 50%;
      display: flex;
      align-items: center;
      justify-content: center;
      color: #fff;
      font-weight: 600;
      font-size: 13px;
    }
    .user-name {
      font-size: 14px;
      font-weight: 500;
    }
  }
  .more-icon {
    cursor: pointer;
    color: var(--text-muted);
    font-size: 14px;
  }
}

.portal-content {
  flex: 1;
  overflow-y: auto;
}
</style>

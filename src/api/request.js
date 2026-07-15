import axios from 'axios'
import { ElMessage } from 'element-plus'
import { useUserStore } from '@/store/user'
import { startMock } from '@/mock'

// 是否启用 mock（开发阶段默认开启；生产环境设为 false 走真实接口）
const USE_MOCK = import.meta.env.VITE_USE_MOCK !== 'false'

const service = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/api',
  timeout: 15000
})

// ============= 请求拦截 =============
service.interceptors.request.use(
  (config) => {
    const userStore = useUserStore()
    const token = config.url?.startsWith('/portal') ? userStore.portalToken : userStore.adminToken
    if (token) {
      config.headers.Authorization = `Bearer ${token}`
    }
    return config
  },
  (err) => Promise.reject(err)
)

// ============= 响应拦截 =============
service.interceptors.response.use(
  (response) => {
    const res = response.data
    if (res.code !== 0) {
      ElMessage.error(res.message || '请求失败')
      return Promise.reject(new Error(res.message || 'Error'))
    }
    return res
  },
  (err) => {
    const status = err.response?.status
    const msg = err.response?.data?.message || err.message || '网络异常'
    if (status === 401) {
      const userStore = useUserStore()
      // 简化处理：过期统一清空认证
      if (err.config?.url?.startsWith('/portal')) userStore.clearPortalAuth()
      else userStore.clearAdminAuth()
      ElMessage.error('登录已过期，请重新登录')
    } else {
      ElMessage.error(msg)
    }
    return Promise.reject(err)
  }
)

// ============= Mock 适配：拦截 axios 并返回模拟数据 =============
if (USE_MOCK && typeof window !== 'undefined') {
  startMock(service)
}

export default service

import { createRouter, createWebHashHistory } from 'vue-router'
import { useUserStore } from '@/store/user'

const AdminLayout = () => import('@/layout/AdminLayout.vue')
const PortalLayout = () => import('@/layout/PortalLayout.vue')

const routes = [
  // 默认重定向到管理端登录
  { path: '/', redirect: '/admin/login' },

  // ============= 管理端 =============
  {
    path: '/admin/login',
    name: 'AdminLogin',
    component: () => import('@/views/admin/Login.vue'),
    meta: { title: '登录', mode: 'admin' }
  },
  {
    path: '/admin',
    component: AdminLayout,
    meta: { requiresAuth: true, mode: 'admin' },
    children: [
      { path: '', redirect: '/admin/customers' },
      {
        path: 'customers',
        name: 'CustomerList',
        component: () => import('@/views/admin/customer/List.vue'),
        meta: { title: '客户管理', icon: 'User' }
      },
      {
        path: 'customers/new',
        name: 'CustomerCreate',
        component: () => import('@/views/admin/customer/Form.vue'),
        meta: { title: '新增客户', activeMenu: '/admin/customers' }
      },
      {
        path: 'customers/:id',
        name: 'CustomerDetail',
        component: () => import('@/views/admin/customer/Detail.vue'),
        meta: { title: '客户详情', activeMenu: '/admin/customers' }
      },
      {
        path: 'customers/:id/edit',
        name: 'CustomerEdit',
        component: () => import('@/views/admin/customer/Form.vue'),
        meta: { title: '编辑客户', activeMenu: '/admin/customers' }
      },
      {
        path: 'opportunities',
        name: 'OpportunityKanban',
        component: () => import('@/views/admin/opportunity/Kanban.vue'),
        meta: { title: '商机管理', icon: 'Aim' }
      },
      {
        path: 'opportunities/new',
        name: 'OpportunityCreate',
        component: () => import('@/views/admin/opportunity/Form.vue'),
        meta: { title: '新增商机', activeMenu: '/admin/opportunities' }
      },
      {
        path: 'opportunities/:id',
        name: 'OpportunityDetail',
        component: () => import('@/views/admin/opportunity/Detail.vue'),
        meta: { title: '商机详情', activeMenu: '/admin/opportunities' }
      },
      {
        path: 'quotations',
        name: 'QuotationList',
        component: () => import('@/views/admin/quotation/List.vue'),
        meta: { title: '报价管理', icon: 'Document' }
      },
      {
        path: 'quotations/new',
        name: 'QuotationCreate',
        component: () => import('@/views/admin/quotation/Form.vue'),
        meta: { title: '新建报价', activeMenu: '/admin/quotations' }
      },
      {
        path: 'quotations/:id',
        name: 'QuotationDetail',
        component: () => import('@/views/admin/quotation/Detail.vue'),
        meta: { title: '报价详情', activeMenu: '/admin/quotations' }
      },
      {
        path: 'bids',
        name: 'BidList',
        component: () => import('@/views/admin/bid/List.vue'),
        meta: { title: '投标管理', icon: 'TrophyBase' }
      },
      {
        path: 'bids/new',
        name: 'BidCreate',
        component: () => import('@/views/admin/bid/Form.vue'),
        meta: { title: '新建投标', activeMenu: '/admin/bids' }
      },
      {
        path: 'bids/:id',
        name: 'BidDetail',
        component: () => import('@/views/admin/bid/Detail.vue'),
        meta: { title: '投标详情', activeMenu: '/admin/bids' }
      }
    ]
  },

  // ============= 客户门户端 =============
  {
    path: '/portal/login',
    name: 'PortalLogin',
    component: () => import('@/views/portal/Login.vue'),
    meta: { title: '客户登录', mode: 'portal' }
  },
  {
    path: '/portal',
    component: PortalLayout,
    meta: { requiresPortalAuth: true, mode: 'portal' },
    children: [
      { path: '', redirect: '/portal/dashboard' },
      {
        path: 'dashboard',
        name: 'PortalDashboard',
        component: () => import('@/views/portal/Dashboard.vue'),
        meta: { title: '工作台' }
      },
      {
        path: 'projects',
        name: 'PortalProjects',
        component: () => import('@/views/portal/Projects.vue'),
        meta: { title: '我的项目' }
      },
      {
        path: 'reports',
        name: 'PortalReports',
        component: () => import('@/views/portal/Reports.vue'),
        meta: { title: '报告中心' }
      },
      {
        path: 'filing',
        name: 'PortalFiling',
        component: () => import('@/views/portal/Filing.vue'),
        meta: { title: '备案信息' }
      },
      {
        path: 'evaluation',
        name: 'PortalEvaluation',
        component: () => import('@/views/portal/Evaluation.vue'),
        meta: { title: '服务评价' }
      },
      {
        path: 'feedback',
        name: 'PortalFeedback',
        component: () => import('@/views/portal/Feedback.vue'),
        meta: { title: '反馈与投诉' }
      }
    ]
  },

  // 404
  {
    path: '/:pathMatch(.*)*',
    name: 'NotFound',
    component: () => import('@/views/NotFound.vue'),
    meta: { title: '页面不存在' }
  }
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
  scrollBehavior: () => ({ left: 0, top: 0 })
})

router.beforeEach((to, from, next) => {
  document.title = to.meta.title ? `${to.meta.title} - 客户与商机管理子系统` : '客户与商机管理子系统'

  const userStore = useUserStore()

  // 管理端鉴权
  if (to.meta.requiresAuth && !userStore.adminToken) {
    return next({ path: '/admin/login', query: { redirect: to.fullPath } })
  }

  // 门户端鉴权
  if (to.meta.requiresPortalAuth && !userStore.portalToken) {
    return next({ path: '/portal/login', query: { redirect: to.fullPath } })
  }

  next()
})

export default router

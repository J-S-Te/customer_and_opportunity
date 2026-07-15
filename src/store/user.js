import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

const ADMIN_TOKEN_KEY = 'crm_admin_token'
const PORTAL_TOKEN_KEY = 'crm_portal_token'
const ADMIN_USER_KEY = 'crm_admin_user'
const PORTAL_USER_KEY = 'crm_portal_user'

function safeParse(json, fallback) {
  try { return JSON.parse(json) || fallback } catch { return fallback }
}

export const useUserStore = defineStore('user', () => {
  const adminToken = ref(localStorage.getItem(ADMIN_TOKEN_KEY) || '')
  const adminUser = ref(safeParse(localStorage.getItem(ADMIN_USER_KEY), null))
  const portalToken = ref(localStorage.getItem(PORTAL_TOKEN_KEY) || '')
  const portalUser = ref(safeParse(localStorage.getItem(PORTAL_USER_KEY), null))

  const isAdminLogged = computed(() => !!adminToken.value)
  const isPortalLogged = computed(() => !!portalToken.value)

  function setAdminAuth(token, user) {
    adminToken.value = token
    adminUser.value = user
    localStorage.setItem(ADMIN_TOKEN_KEY, token)
    localStorage.setItem(ADMIN_USER_KEY, JSON.stringify(user))
  }

  function clearAdminAuth() {
    adminToken.value = ''
    adminUser.value = null
    localStorage.removeItem(ADMIN_TOKEN_KEY)
    localStorage.removeItem(ADMIN_USER_KEY)
  }

  function setPortalAuth(token, user) {
    portalToken.value = token
    portalUser.value = user
    localStorage.setItem(PORTAL_TOKEN_KEY, token)
    localStorage.setItem(PORTAL_USER_KEY, JSON.stringify(user))
  }

  function clearPortalAuth() {
    portalToken.value = ''
    portalUser.value = null
    localStorage.removeItem(PORTAL_TOKEN_KEY)
    localStorage.removeItem(PORTAL_USER_KEY)
  }

  return {
    adminToken, adminUser, portalToken, portalUser,
    isAdminLogged, isPortalLogged,
    setAdminAuth, clearAdminAuth, setPortalAuth, clearPortalAuth
  }
})

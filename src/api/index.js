import request from './request'

// ================ 客户管理 ================

export const customerApi = {
  list: (params) => request({ url: '/customers', method: 'get', params }),
  detail: (id) => request({ url: `/customers/${id}`, method: 'get' }),
  create: (data) => request({ url: '/customers', method: 'post', data }),
  update: (id, data) => request({ url: `/customers/${id}`, method: 'put', data }),
  checkDuplicate: (data) => request({ url: '/customers/check-duplicate', method: 'post', data }),
  merge: (sourceId, targetId) =>
    request({ url: `/customers/${sourceId}/merge-to/${targetId}`, method: 'post' }),
  archive: (id, data) => request({ url: `/customers/${id}/archive`, method: 'put', data }),
  recalculateCredit: (id) =>
    request({ url: `/customers/${id}/recalculate-credit`, method: 'post' }),
  exportExcel: (params) =>
    request({ url: '/customers/export', method: 'get', params, responseType: 'blob' }),
  logs: (id, params) => request({ url: `/customers/${id}/logs`, method: 'get', params }),
  adjustCredit: (id, data) =>
    request({ url: `/customers/${id}/credit-adjust`, method: 'post', data })
}

// ================ 商机管理 ================

export const opportunityApi = {
  list: (params) => request({ url: '/opportunities', method: 'get', params }),
  detail: (id) => request({ url: `/opportunities/${id}`, method: 'get' }),
  create: (data) => request({ url: '/opportunities', method: 'post', data }),
  update: (id, data) => request({ url: `/opportunities/${id}`, method: 'put', data }),
  addFollow: (id, data) =>
    request({ url: `/opportunities/${id}/follows`, method: 'post', data }),
  advanceStage: (id, data) =>
    request({ url: `/opportunities/${id}/advance-stage`, method: 'post', data }),
  markLost: (id, data) =>
    request({ url: `/opportunities/${id}/mark-lost`, method: 'post', data }),
  transferToContract: (id) =>
    request({ url: `/opportunities/${id}/transfer-to-contract`, method: 'post' })
}

// ================ 报价管理 ================

export const quotationApi = {
  list: (params) => request({ url: '/quotations', method: 'get', params }),
  detail: (id) => request({ url: `/quotations/${id}`, method: 'get' }),
  create: (data) => request({ url: '/quotations', method: 'post', data }),
  update: (id, data) => request({ url: `/quotations/${id}`, method: 'put', data }),
  submitApproval: (id) =>
    request({ url: `/quotations/${id}/submit-approval`, method: 'post' }),
  approve: (quotationId, nodeId, data) =>
    request({ url: `/quotations/${quotationId}/approvals/${nodeId}`, method: 'put', data }),
  transferToContract: (id) =>
    request({ url: `/quotations/${id}/transfer-to-contract`, method: 'post' })
}

// ================ 投标管理 ================

export const bidApi = {
  list: (params) => request({ url: '/bids', method: 'get', params }),
  detail: (id) => request({ url: `/bids/${id}`, method: 'get' }),
  create: (data) => request({ url: '/bids', method: 'post', data }),
  update: (id, data) => request({ url: `/bids/${id}`, method: 'put', data }),
  stats: () => request({ url: '/bids/stats', method: 'get' }),
  recordResult: (id, data) =>
    request({ url: `/bids/${id}/bid-result`, method: 'put', data }),
  refundDeposit: (id, data) =>
    request({ url: `/bids/${id}/deposit/refund`, method: 'put', data })
}

// ================ 门户端 ================

export const portalApi = {
  login: (data) => request({ url: '/portal/login', method: 'post', data }),
  dashboard: () => request({ url: '/portal/dashboard', method: 'get' }),
  projects: () => request({ url: '/portal/projects', method: 'get' }),
  projectProgress: (id) =>
    request({ url: `/portal/projects/${id}/progress`, method: 'get' }),
  reportRequest: (data) =>
    request({ url: '/portal/reports/request', method: 'post', data }),
  reportList: () => request({ url: '/portal/reports/requests', method: 'get' }),
  reportDownload: (id) =>
    request({ url: `/portal/reports/download/${id}`, method: 'get', responseType: 'blob' }),
  filingSubmit: (data) => request({ url: '/portal/filing', method: 'post', data }),
  filingPdf: (id) =>
    request({ url: `/portal/filing/${id}/pdf`, method: 'get', responseType: 'blob' }),
  submitEvaluation: (data) =>
    request({ url: '/portal/evaluations', method: 'post', data }),
  submitFeedback: (data) =>
    request({ url: '/portal/feedbacks', method: 'post', data })
}

// ================ 通用 ================

export const commonApi = {
  upload: (formData) =>
    request({ url: '/files/upload', method: 'post', data: formData, headers: { 'Content-Type': 'multipart/form-data' } }),
  enums: () => request({ url: '/dict/enums', method: 'get' }),
  me: () => request({ url: '/auth/me', method: 'get' })
}

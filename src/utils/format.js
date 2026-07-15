import dayjs from 'dayjs'

export function formatDate(value, fmt = 'YYYY-MM-DD') {
  if (!value) return '--'
  return dayjs(value).format(fmt)
}

export function formatDateTime(value) {
  return formatDate(value, 'YYYY-MM-DD HH:mm:ss')
}

export function formatMoney(yuan, prefix = '¥', decimals = 2) {
  if (yuan == null) return '--'
  return `${prefix}${Number(yuan).toLocaleString('zh-CN', {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals
  })}`
}

export function formatWan(yuan, decimals = 1) {
  if (yuan == null) return '--'
  return `${(Number(yuan) / 10000).toFixed(decimals)}万`
}

export function formatAmount(yuan) {
  if (!yuan) return '0.00'
  const num = Number(yuan)
  if (num >= 100000000) return `${(num / 100000000).toFixed(2)}亿`
  if (num >= 10000) return `${(num / 10000).toFixed(2)}万`
  return num.toLocaleString()
}

export function fromNow(value) {
  if (!value) return '--'
  const diff = Date.now() - new Date(value).getTime()
  const sec = Math.floor(diff / 1000)
  if (sec < 60) return `${sec} 秒前`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min} 分钟前`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr} 小时前`
  const day = Math.floor(hr / 24)
  if (day < 30) return `${day} 天前`
  return formatDate(value)
}

export function maskPhone(phone) {
  if (!phone) return '--'
  return String(phone).replace(/(\d{3})\d{4}(\d{4})/, '$1****$2')
}

export function maskBankAccount(acc) {
  if (!acc) return '--'
  return acc.length > 8 ? acc.slice(0, 4) + '****' + acc.slice(-4) : acc
}

export function debounce(fn, delay = 300) {
  let timer
  return function (...args) {
    clearTimeout(timer)
    timer = setTimeout(() => fn.apply(this, args), delay)
  }
}

export function throttle(fn, delay = 300) {
  let last = 0
  return function (...args) {
    const now = Date.now()
    if (now - last >= delay) {
      last = now
      fn.apply(this, args)
    }
  }
}

export function genBizNo(prefix) {
  const now = new Date()
  const ymd = `${now.getFullYear()}${String(now.getMonth() + 1).padStart(2, '0')}${String(now.getDate()).padStart(2, '0')}`
  const rand = String(Math.floor(Math.random() * 9999) + 1).padStart(4, '0')
  return `${prefix}${ymd}${rand}`
}

export function validateCreditCode(code) {
  if (!code || code.length !== 18) return false
  return /^[0-9A-HJ-NPQRTUWXY]{18}$/i.test(code)
}

export function validatePhone(phone) {
  return /^1[3-9]\d{9}$/.test(phone)
}

export function validateEmail(email) {
  return /^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$/.test(email)
}

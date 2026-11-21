// 配额模型分组归类、健康度判定、倒计时与 Token 紧凑格式化工具。
import type { Account, ModelQuota } from '@/types/models'

export interface ModelGroupQuotaSummary {
  key: string
  label: string
  percentage: number
  resetTime: string
  modelCount: number
}

// 将 CloudCode 返回的原始模型列表按主系列归纳，用于卡片和表格简洁展示。
export function summarizeModelGroups(account: Account): ModelGroupQuotaSummary[] {
  const models = account.quota?.models ?? []
  if (models.length === 0) return []

  const buckets = new Map<string, { label: string; items: ModelQuota[] }>()

  for (const m of models) {
    const lower = m.name.toLowerCase()
    let key = 'other'
    let label = '其他模型'

    if (lower.includes('claude')) {
      key = 'claude'
      label = 'Claude'
    } else if (lower.includes('flash')) {
      key = 'gemini-flash'
      label = 'Gemini Flash'
    } else if (lower.includes('gemini') || lower.includes('argon') || lower.includes('carbon')) {
      key = 'gemini-pro'
      label = 'Gemini Pro'
    } else if (lower.includes('gpt')) {
      key = 'gpt'
      label = 'GPT'
    }

    const existing = buckets.get(key)
    if (existing) {
      existing.items.push(m)
    } else {
      buckets.set(key, { label, items: [m] })
    }
  }

  const order = ['gemini-pro', 'claude', 'gemini-flash', 'gpt', 'other']
  const result: ModelGroupQuotaSummary[] = []

  for (const key of order) {
    const group = buckets.get(key)
    if (!group || group.items.length === 0) continue
    // 以同组中剩余最低配额代表该组当前可用水位
    let minPct = 100
    let earliestReset = ''
    for (const item of group.items) {
      if (item.percentage < minPct) {
        minPct = item.percentage
      }
      if (item.resetTime) {
        if (!earliestReset || item.resetTime < earliestReset) {
          earliestReset = item.resetTime
        }
      }
    }
    result.push({
      key,
      label: group.label,
      percentage: minPct,
      resetTime: earliestReset,
      modelCount: group.items.length,
    })
  }

  return result
}

// 返回账号综合最低配额百分比；若尚未拉取配额则返回 -1。
export function getMinQuotaPercentage(account: Account): number {
  const models = account.quota?.models ?? []
  if (models.length === 0) return -1
  let min = 100
  for (const m of models) {
    if (m.percentage < min) min = m.percentage
  }
  return min
}

// 判断账号是否处于「异常」状态（禁用、403 封禁、或存在配额/验证报错）。
export function isAccountAbnormal(account: Account): boolean {
  if (account.disabled) return true
  if (account.quota?.isForbidden) return true
  if (account.quotaError && account.quotaError.message) return true
  return false
}

// 判断账号是否处于「低配额」状态（未异常且已拉取配额，最低剩余百分比 <= 阈值）。
export function isAccountLowQuota(account: Account, threshold = 20): boolean {
  if (isAccountAbnormal(account)) return false
  const minPct = getMinQuotaPercentage(account)
  return minPct >= 0 && minPct <= threshold
}

// 获取账号最早的配额重置时间文本。
export function getEarliestResetTime(account: Account): string {
  const models = account.quota?.models ?? []
  let earliest = ''
  for (const m of models) {
    if (m.resetTime && (!earliest || m.resetTime < earliest)) {
      earliest = m.resetTime
    }
  }
  return earliest
}

// 将 ISO 时间戳格式化为简洁剩余倒计时（如 2h 14m / 35m / 已就绪）。
export function formatResetCountdown(iso?: string): string {
  if (!iso) return '—'
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return iso
  const diffSec = Math.floor((ts - Date.now()) / 1000)
  if (diffSec <= 0) return '即将重置'

  const days = Math.floor(diffSec / 86400)
  const hours = Math.floor((diffSec % 86400) / 3600)
  const mins = Math.floor((diffSec % 3600) / 60)

  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${mins}m`
  return `${Math.max(1, mins)}m`
}

// 格式化完整本地日期时间。
export function formatDateTime(ts: number | string | undefined): string {
  if (!ts) return '—'
  let date: Date
  if (typeof ts === 'number') {
    // 兼容秒级与毫秒级 Unix 时间戳
    date = new Date(ts < 1e12 ? ts * 1000 : ts)
  } else {
    date = new Date(ts)
  }
  if (Number.isNaN(date.getTime())) return String(ts)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

// 返回配额进度条对应的语义色变量名。
export function getQuotaTone(percentage: number): 'success' | 'warning' | 'danger' {
  if (percentage >= 60) return 'success'
  if (percentage >= 20) return 'warning'
  return 'danger'
}

// 将 Token 数值格式化为紧凑可读格式（如 1.24M / 48.5K）。
export function formatTokenCount(n: number | undefined): string {
  const val = Number(n ?? 0)
  if (!Number.isFinite(val) || val <= 0) return '0'
  if (val >= 1_000_000_000) return `${(val / 1_000_000_000).toFixed(2)}B`
  if (val >= 1_000_000) return `${(val / 1_000_000).toFixed(2)}M`
  if (val >= 10_000) return `${(val / 1_000).toFixed(1)}K`
  if (val >= 1_000) return `${(val / 1_000).toFixed(2)}K`
  return String(Math.round(val))
}

export function formatExactNumber(n: number | undefined): string {
  return new Intl.NumberFormat('zh-CN').format(Math.round(Number(n ?? 0)))
}

// 返回规范化订阅等级文案。
export function getTierLabel(account: Account): 'ULTRA' | 'PRO' | 'FREE' | 'UNKNOWN' {
  const tier = (account.quota?.subscriptionTier ?? '').toUpperCase()
  if (tier === 'ULTRA' || tier === 'PRO' || tier === 'FREE') return tier
  return 'UNKNOWN'
}

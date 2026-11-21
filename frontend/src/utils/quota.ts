// 配额模型分组归类、同模型思考深度合并、健康度判定、倒计时与 Token 紧凑格式化工具。
import type { Account, ModelQuota } from '@/types/models'

export interface ModelGroupQuotaSummary {
  key: string
  label: string
  percentage: number
  resetTime: string
  modelCount: number
}

export interface MergedModelVariant {
  name: string
  displayName: string
  depthLabel: string
  depthRank: number
  percentage: number
  resetTime: string
}

export interface MergedModelQuota {
  key: string
  baseId: string
  displayName: string
  percentage: number
  resetTime: string
  hasMixedPercentages: boolean
  canonicalModelId: string
  variants: MergedModelVariant[]
}

const ID_DEPTH_SUFFIX_RE = /(?:[-_])(thinking|minimal|low|medium|med|high|xhigh|extra[-_]?high|max)$/i
const DISPLAY_PAREN_DEPTH_RE =
  /\s*[（(]\s*((?:thinking|minimal|low|medium|med|high|xhigh|extra\s*high|max)(?:\s*[-/,·&\s]+\s*(?:thinking|minimal|low|medium|med|high|xhigh|extra\s*high|max))*)\s*[)）]\s*$/i
const DISPLAY_TRAILING_DEPTH_RE =
  /(?:\s*[-–—]\s*|\s+)(thinking|minimal|low|medium|high|xhigh|extra\s*high)\s*$/i

function normalizeDepthToken(raw: string): { label: string; rank: number } | null {
  const token = raw.trim().toLowerCase().replace(/[\s_-]+/g, '')
  switch (token) {
    case 'minimal':
      return { label: 'Minimal', rank: 1 }
    case 'low':
      return { label: 'Low', rank: 2 }
    case 'medium':
    case 'med':
      return { label: 'Medium', rank: 3 }
    case 'high':
      return { label: 'High', rank: 4 }
    case 'xhigh':
    case 'extrahigh':
      return { label: 'XHigh', rank: 5 }
    case 'max':
      return { label: 'Max', rank: 6 }
    case 'thinking':
      return { label: 'Thinking', rank: 7 }
    default:
      return null
  }
}

function stripDepthFromModelId(rawId: string): { baseId: string; tokens: string[] } {
  let base = rawId.trim()
  const collected: string[] = []
  while (true) {
    const match = base.match(ID_DEPTH_SUFFIX_RE)
    if (!match || match.index === undefined) break
    const candidate = base.slice(0, match.index).trim()
    if (candidate.length < 3) break
    collected.push(match[1])
    base = candidate
  }
  return { baseId: base, tokens: collected.reverse() }
}

function stripDepthFromDisplayName(rawDisplay: string): { baseDisplay: string; tokens: string[] } {
  let base = rawDisplay.trim()
  const collected: string[] = []
  while (base) {
    const parenMatch = base.match(DISPLAY_PAREN_DEPTH_RE)
    if (parenMatch && parenMatch.index !== undefined) {
      const candidate = base.slice(0, parenMatch.index).trim()
      if (!candidate) break
      const parts = parenMatch[1]
        .replace(/extra\s+high/gi, 'extrahigh')
        .split(/[-/,·&\s]+/)
        .filter(Boolean)
      collected.unshift(...parts)
      base = candidate
      continue
    }
    const trailingMatch = base.match(DISPLAY_TRAILING_DEPTH_RE)
    if (trailingMatch && trailingMatch.index !== undefined) {
      const candidate = base.slice(0, trailingMatch.index).trim()
      if (!candidate) break
      collected.unshift(trailingMatch[1])
      base = candidate
      continue
    }
    break
  }
  return { baseDisplay: base, tokens: collected }
}

function buildDepthInfo(tokens: string[]): { label: string; rank: number } {
  const parsed = tokens
    .map(normalizeDepthToken)
    .filter((item): item is { label: string; rank: number } => item !== null)
  if (parsed.length === 0) {
    return { label: '标准', rank: 0 }
  }

  // 去重并按语义排序（先具体深度再 Thinking，或在组合时清晰拼接）
  const uniqueMap = new Map<string, { label: string; rank: number }>()
  for (const item of parsed) {
    if (!uniqueMap.has(item.label)) {
      uniqueMap.set(item.label, item)
    }
  }
  const unique = Array.from(uniqueMap.values()).sort((a, b) => a.rank - b.rank)
  const nonThinking = unique.filter((item) => item.label !== 'Thinking')
  const hasThinking = unique.some((item) => item.label === 'Thinking')

  if (nonThinking.length > 0 && hasThinking) {
    return {
      label: `Thinking · ${nonThinking.map((i) => i.label).join('/')}`,
      rank: nonThinking[0].rank + 0.5,
    }
  }
  return {
    label: unique.map((i) => i.label).join(' · '),
    rank: unique[0].rank,
  }
}

function canonicalMergeKey(raw: string): string {
  return raw
    .trim()
    .toLowerCase()
    // 统一版本号分隔符（如 3.1 与 3-1、4.5 与 4-5），确保有无 displayName 时都能命中同一模型
    .replace(/(\d)\.(\d)/g, '$1-$2')
    .replace(/[^a-z0-9:]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

// 将同一模型的不同思考深度（如 Low / Medium / High / XHigh / Thinking）合并为单一模型条目，
// 同时完整保留每个原始变体的 ID 与剩余配额，避免丢失差异或影响唤醒 API 真实模型 ID。
export function mergeModelList(models: ModelQuota[]): MergedModelQuota[] {
  if (!models || models.length === 0) return []

  interface Accumulator {
    key: string
    baseIdCandidates: string[]
    baseDisplayCandidates: string[]
    variants: MergedModelVariant[]
  }

  const groups = new Map<string, Accumulator>()

  for (const m of models) {
    const rawName = (m.name ?? '').trim()
    if (!rawName) continue
    const rawDisplay = (m.displayName ?? '').trim()

    const idInfo = stripDepthFromModelId(rawName)
    const displayInfo = stripDepthFromDisplayName(rawDisplay)
    const depth = buildDepthInfo(displayInfo.tokens.length > 0 ? displayInfo.tokens : idInfo.tokens)

    const keySource = displayInfo.baseDisplay || idInfo.baseId || rawName
    const key = canonicalMergeKey(keySource) || rawName.toLowerCase()

    const variant: MergedModelVariant = {
      name: rawName,
      displayName: rawDisplay || rawName,
      depthLabel: depth.label,
      depthRank: depth.rank,
      percentage: m.percentage,
      resetTime: (m.resetTime ?? '').trim(),
    }

    const existing = groups.get(key)
    if (existing) {
      existing.variants.push(variant)
      if (idInfo.baseId) existing.baseIdCandidates.push(idInfo.baseId)
      if (displayInfo.baseDisplay) existing.baseDisplayCandidates.push(displayInfo.baseDisplay)
    } else {
      groups.set(key, {
        key,
        baseIdCandidates: idInfo.baseId ? [idInfo.baseId] : [],
        baseDisplayCandidates: displayInfo.baseDisplay ? [displayInfo.baseDisplay] : [],
        variants: [variant],
      })
    }
  }

  const result: MergedModelQuota[] = []

  for (const acc of groups.values()) {
    acc.variants.sort((a, b) => {
      if (a.depthRank !== b.depthRank) return a.depthRank - b.depthRank
      return a.name.localeCompare(b.name)
    })

    let minPct = 100
    let earliestReset = ''
    const firstPct = acc.variants[0].percentage
    let hasMixedPercentages = false

    for (const v of acc.variants) {
      if (v.percentage < minPct) minPct = v.percentage
      if (v.percentage !== firstPct) hasMixedPercentages = true
      if (v.resetTime && (!earliestReset || v.resetTime < earliestReset)) {
        earliestReset = v.resetTime
      }
    }

    const preferredBaseId =
      acc.baseIdCandidates.find((id) => !id.toUpperCase().startsWith('MODEL_PLACEHOLDER_')) ||
      acc.baseIdCandidates[0] ||
      acc.variants[0].name

    const cleanDisplay = acc.baseDisplayCandidates[0] || preferredBaseId
    const finalDisplayName =
      acc.variants.length > 1 ? cleanDisplay : acc.variants[0].displayName || cleanDisplay

    result.push({
      key: acc.key,
      baseId: acc.variants.length > 1 ? preferredBaseId : acc.variants[0].name,
      displayName: finalDisplayName,
      percentage: minPct,
      resetTime: earliestReset,
      hasMixedPercentages,
      canonicalModelId: acc.variants[0].name,
      variants: acc.variants,
    })
  }

  result.sort((a, b) => a.displayName.localeCompare(b.displayName))
  return result
}

export function mergeAccountModels(account: Account | null | undefined): MergedModelQuota[] {
  return mergeModelList(account?.quota?.models ?? [])
}

// 将 CloudCode 返回的模型列表按主系列归纳，用于卡片和表格简洁展示。
export function summarizeModelGroups(account: Account): ModelGroupQuotaSummary[] {
  const models = mergeAccountModels(account)
  if (models.length === 0) return []

  const buckets = new Map<string, { label: string; items: MergedModelQuota[] }>()

  for (const m of models) {
    const lower = `${m.baseId} ${m.displayName}`.toLowerCase()
    let key = 'other'
    let label = '其他模型'

    if (lower.includes('claude')) {
      key = 'claude'
      label = 'Claude'
    } else if (lower.includes('flash') || lower.includes('hydrogen')) {
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

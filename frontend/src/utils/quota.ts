// 配额分类（Gemini Argon / Gemini Flash / Claude/GPT，含 5h 与周额度双窗口）、同模型思考深度合并、健康度判定与格式化工具。
import type { Account, ModelQuota, QuotaBucket } from '@/types/models'

export interface QuotaWindowSlot {
  percentage: number
  resetTime: string
  exhausted?: boolean
}

export type QuotaCategoryKey = 'gemini-argon' | 'gemini-flash' | 'claude-gpt'

export interface QuotaCategorySummary {
  key: QuotaCategoryKey
  label: string
  shortLabel: string
  fiveHour?: QuotaWindowSlot
  weekly?: QuotaWindowSlot
  /** 凡是有 5 小时限额的分类必然也有周限额；为 true 时始终保留双窗口展示（尚未拉到周桶时显示待同步占位）。 */
  expectsWeekly: boolean
  percentage: number
  resetTime: string
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

const SUMMARY_BUCKET_ID_SET = new Set([
  '3p-5h',
  '3p-weekly',
  'claude:5h',
  'claude:weekly',
  'claude-5h',
  'claude-weekly',
  'gemini-5h',
  'gemini-weekly',
  'gemini:5h',
  'gemini:weekly',
  'gemini-flash-5h',
  'gemini-flash-weekly',
  'flash:5h',
  'flash:weekly',
  'gemini-argon-5h',
  'argon:5h',
  'argon:weekly',
  'gemini-argon-wtu-weekly',
  'carbon:weekly',
  'gemini-carbon-wtu-weekly',
])

export function isSummaryBucketId(rawName: string): boolean {
  const lower = rawName.trim().toLowerCase()
  if (!lower) return false
  if (SUMMARY_BUCKET_ID_SET.has(lower)) return true
  if (lower.startsWith('gemini-argon-') && (lower.endsWith('-weekly') || lower.endsWith('-5h'))) {
    return true
  }
  return false
}

function isExcludedNonAntigravityModel(rawName: string, rawDisplay: string): boolean {
  if (isSummaryBucketId(rawName)) return true
  const norm = (s: string) => s.trim().toLowerCase().replace(/[.\s]+/g, '-')
  const n = norm(rawName)
  const d = norm(rawDisplay)
  const excludedPrefixes = [
    'gemini-2-5-pro',
    'gemini-3-1-flash-image',
    'gemini-3-1-flash-lite',
    'gemini-3-5-flash-lite',
  ]
  for (const target of excludedPrefixes) {
    if (n.includes(target) || d.includes(target)) return true
  }
  for (const candidate of [n, d]) {
    if (
      candidate === 'gemini-3-flash' ||
      candidate.startsWith('gemini-3-flash-') ||
      candidate.startsWith('gemini-3-flash(')
    ) {
      return true
    }
  }
  return false
}

// 将同一模型的不同思考深度（如 Low / Medium / High / XHigh / Thinking）合并为单一模型条目，
// 同时过滤掉非反重力客户端模型（如 Gemini 3 Flash、Flash Image、Flash Lite）。
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
    if (isExcludedNonAntigravityModel(rawName, rawDisplay)) continue

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

  // 保留云端 agentModelSorts 下发的原始展示顺序，不再强行按字母重排
  return result
}

export function mergeAccountModels(account: Account | null | undefined): MergedModelQuota[] {
  return mergeModelList(account?.quota?.models ?? [])
}

function classifySummaryBucket(bucketId: string): { key: QuotaCategoryKey; period: 'fiveHour' | 'weekly' } | null {
  const id = bucketId.trim().toLowerCase()
  if (!id) return null
  const is5h = id.endsWith('5h') || id.includes(':5h') || id.includes('-5h')
  const isWeekly = id.includes('weekly') || id.endsWith('7d') || id.includes(':week')
  if (!is5h && !isWeekly) return null
  const period: 'fiveHour' | 'weekly' = isWeekly ? 'weekly' : 'fiveHour'

  if (id.includes('argon')) {
    return { key: 'gemini-argon', period }
  }
  if (id.includes('3p') || id.includes('claude') || id.includes('gpt')) {
    return { key: 'claude-gpt', period }
  }
  if (
    (id.includes('gemini') || id.includes('flash') || id.includes('neon') || id.includes('hydrogen')) &&
    !id.includes('carbon')
  ) {
    return { key: 'gemini-flash', period }
  }
  return null
}

function classifyModelCategory(m: MergedModelQuota): QuotaCategoryKey | null {
  const lower = `${m.baseId} ${m.displayName}`.toLowerCase()
  if (lower.includes('argon')) return 'gemini-argon'
  if (lower.includes('claude') || lower.includes('gpt')) return 'claude-gpt'
  if (lower.includes('flash') || lower.includes('neon') || lower.includes('hydrogen')) {
    return 'gemini-flash'
  }
  if (lower.includes('gemini') && !lower.includes('carbon') && !lower.includes('image')) {
    return 'gemini-flash'
  }
  return null
}

function isWeeklyResetTime(iso?: string): boolean {
  if (!iso) return false
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return false
  // 距离重置超过 12 小时通常为周额度窗口（5h 窗口上限为 5 小时）
  return ts - Date.now() > 12 * 3600 * 1000
}

// 汇总当前账号的三大配额分类（Gemini Argon / Gemini Flash / Claude/GPT，不含 Pro）：
// 优先使用云端 retrieveUserQuotaSummary 返回的 5h 与周额度桶；两者都有则双窗口并列显示，只有周额度则仅显示周额度。
export function summarizeQuotaCategories(account: Account | null | undefined): QuotaCategorySummary[] {
  if (!account?.quota) return []

  const isFree = getTierLabel(account) === 'FREE'
  const categoryDefs: Array<{ key: QuotaCategoryKey; label: string; shortLabel: string }> = [
    { key: 'gemini-argon', label: 'Gemini Argon', shortLabel: 'Argon' },
    { key: 'gemini-flash', label: 'Gemini Flash', shortLabel: 'Flash' },
    { key: 'claude-gpt', label: 'Claude/GPT', shortLabel: 'Claude/GPT' },
  ]

  const state = new Map<QuotaCategoryKey, { fiveHour?: QuotaWindowSlot; weekly?: QuotaWindowSlot }>()
  for (const def of categoryDefs) {
    state.set(def.key, {})
  }

  // 1. 优先消费云端 summaryBuckets（同时兼容旧缓存中落入 models 的 summary bucket 条目）
  const rawBuckets: QuotaBucket[] = [...(account.quota.summaryBuckets ?? [])]
  for (const m of account.quota.models ?? []) {
    if (isSummaryBucketId(m.name)) {
      rawBuckets.push({
        bucketId: m.name,
        displayName: m.displayName,
        percentage: m.percentage,
        resetTime: m.resetTime,
        exhausted: m.percentage <= 0,
      })
    }
  }

  for (const b of rawBuckets) {
    const matched = classifySummaryBucket(b.bucketId)
    if (!matched) continue
    const entry = state.get(matched.key)!
    const slot: QuotaWindowSlot = {
      percentage: b.percentage,
      resetTime: (b.resetTime ?? '').trim(),
      exhausted: b.exhausted || b.percentage <= 0,
    }
    if (matched.period === 'fiveHour') {
      if (!entry.fiveHour || slot.percentage < entry.fiveHour.percentage) {
        entry.fiveHour = slot
      }
    } else {
      if (!entry.weekly || slot.percentage < entry.weekly.percentage) {
        entry.weekly = slot
      }
    }
  }

  // 免费账号在已有周额度时仅展示周额度，不展示 5h 额度
  if (isFree) {
    for (const entry of state.values()) {
      if (entry.weekly) {
        entry.fiveHour = undefined
      }
    }
  }

  // 2. 若某个分类尚未拉取到 summaryBuckets，则从该分类下的真实模型额度兜底推导
  const mergedModels = mergeAccountModels(account)
  const fallbackByCat = new Map<QuotaCategoryKey, MergedModelQuota[]>()
  for (const m of mergedModels) {
    const cat = classifyModelCategory(m)
    if (!cat) continue
    const list = fallbackByCat.get(cat)
    if (list) list.push(m)
    else fallbackByCat.set(cat, [m])
  }

  for (const def of categoryDefs) {
    const entry = state.get(def.key)!
    if (entry.fiveHour || entry.weekly) continue
    const items = fallbackByCat.get(def.key)
    if (!items || items.length === 0) continue

    let minPct = 100
    let earliestReset = ''
    for (const item of items) {
      if (item.percentage < minPct) minPct = item.percentage
      if (item.resetTime && (!earliestReset || item.resetTime < earliestReset)) {
        earliestReset = item.resetTime
      }
    }
    const slot: QuotaWindowSlot = {
      percentage: minPct,
      resetTime: earliestReset,
      exhausted: minPct <= 0,
    }
    if (def.key === 'gemini-argon' || isFree || isWeeklyResetTime(earliestReset)) {
      entry.weekly = slot
    } else {
      entry.fiveHour = slot
    }
  }

  const result: QuotaCategorySummary[] = []
  for (const def of categoryDefs) {
    const entry = state.get(def.key)!
    if (!entry.fiveHour && !entry.weekly) continue

    const slots = [entry.fiveHour, entry.weekly].filter((s): s is QuotaWindowSlot => Boolean(s))
    let minPct = 100
    let earliestReset = ''
    for (const s of slots) {
      if (s.percentage < minPct) minPct = s.percentage
      if (s.resetTime && (!earliestReset || s.resetTime < earliestReset)) {
        earliestReset = s.resetTime
      }
    }

    result.push({
      key: def.key,
      label: def.label,
      shortLabel: def.shortLabel,
      fiveHour: entry.fiveHour,
      weekly: entry.weekly,
      expectsWeekly: Boolean(entry.weekly || entry.fiveHour),
      percentage: minPct,
      resetTime: earliestReset,
    })
  }

  return result
}

// 返回账号综合最低配额百分比（同时覆盖 5h 与周额度窗口）；若尚未拉取配额则返回 -1。
export function getMinQuotaPercentage(account: Account): number {
  const categories = summarizeQuotaCategories(account)
  if (categories.length > 0) {
    let min = 100
    for (const cat of categories) {
      if (cat.percentage < min) min = cat.percentage
    }
    return min
  }
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

// 获取账号最早的配额重置时间文本（同时覆盖 5h / 周额度与模型明细）。
export function getEarliestResetTime(account: Account): string {
  let earliest = ''
  for (const cat of summarizeQuotaCategories(account)) {
    if (cat.resetTime && (!earliest || cat.resetTime < earliest)) {
      earliest = cat.resetTime
    }
  }
  if (earliest) return earliest
  const models = account.quota?.models ?? []
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

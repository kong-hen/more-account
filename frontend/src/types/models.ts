// 前后端共享数据模型，字段名与 Go JSON tag 保持 100% 一致。

export interface TokenData {
  accessToken: string
  refreshToken: string
  expiresIn: number
  expiryTimestamp: number
  tokenType: string
  email?: string
  projectId?: string
  isGcpTos?: boolean | null
  oauthClientKey?: string
  idToken?: string
  sessionId?: string
}

export interface ModelQuota {
  name: string
  displayName?: string
  percentage: number
  resetTime?: string
}

export interface CreditInfo {
  creditType: string
  creditAmount?: string
  minimumCreditAmountForUsage?: string
}

export interface QuotaData {
  models: ModelQuota[]
  lastUpdated: number
  isForbidden: boolean
  subscriptionTier?: string
  credits?: CreditInfo[]
  tierId?: string
  isGcpTos?: boolean | null
  projectId?: string
}

export interface QuotaErrorInfo {
  code?: number
  message: string
  reason?: string
  validationUrl?: string
  timestamp: number
}

export interface Account {
  id: string
  email: string
  name?: string
  groupId?: string
  notes?: string
  accountPassword?: string
  twoFactorSecret?: string
  token: TokenData
  quota?: QuotaData | null
  disabled: boolean
  disabledReason?: string
  disabledAt?: number
  quotaError?: QuotaErrorInfo | null
  sortOrder: number
  createdAt: number
  lastUsed: number
}

export interface AccountDetailsUpdate {
  notes: string
  accountPassword: string
  twoFactorSecret: string
  groupId: string
}

export interface AccountGroup {
  id: string
  name: string
  color: string
  sortOrder: number
  createdAt: number
}

export type ThemeMode = 'light' | 'dark'
export type RuntimeTarget = 'desktop' | 'ide' | 'cli'
export type AppSection = 'accounts' | 'usage' | 'wakeup' | 'history'

export interface AppSettings {
  theme: ThemeMode
  privacyMode: boolean
  runtimeTarget: RuntimeTarget
  launchOnSwitch: boolean
  lowQuotaThreshold: number
  desktopAppPath?: string
  ideAppPath?: string
  tokenUsageDataDir?: string
}

export interface ClientInstallInfo {
  desktopPath: string
  idePath: string
  desktopOk: boolean
  ideOk: boolean
  cliDirOk: boolean
}

export interface BootstrapState {
  accounts: Account[]
  currentAccountId: string
  groups: AccountGroup[]
  settings: AppSettings
  clientInfo: ClientInstallInfo
  dataDir: string
}

export interface RefreshStats {
  total: number
  success: number
  failed: number
  errors?: string[]
}

export interface TotpCodeResult {
  code: string
  period: number
  remainingSeconds: number
  validUntilUnix: number
}

export interface OAuthLoginSession {
  authUrl: string
  redirectUri: string
  state: string
}

export interface SwitchRequest {
  accountId: string
  targetClient: RuntimeTarget
  launch: boolean
}

export interface SwitchOutcome {
  account: Account
  targetClient: RuntimeTarget
  launched: boolean
  launchWarning?: string
  durationMs: number
}

export interface SwitchHistoryItem {
  id: string
  timestamp: number
  accountId: string
  targetEmail: string
  targetClient: RuntimeTarget
  launched: boolean
  success: boolean
  durationMs: number
  errorStage?: string
  errorMessage?: string
}

export interface BatchImportFailed {
  source: string
  error: string
}

export interface BatchImportResult {
  imported: Account[]
  failed: BatchImportFailed[]
}

export interface ExactTokenTotals {
  inputTokens: number
  outputTokens: number
  thinkingTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  totalTokens: number
}

export interface EstimatedTokenTotals {
  inputTokens: number
  outputTokens: number
  totalTokens: number
  estimatedTurnCount: number
}

export interface TurnUsageRecord {
  stepIndex: number
  timestamp?: string
  modelId: string
  modelDisplayName: string
  apiProvider: string
  isExact: boolean
  source: string
  status: number
  exact: ExactTokenTotals
  estimated: EstimatedTokenTotals
  messageId?: string
}

export interface ConversationModelSlice {
  modelId: string
  modelDisplayName: string
  apiProvider: string
  turnCount: number
  exact: ExactTokenTotals
  estimatedTotalTokens: number
}

export interface ConversationSummaryItem {
  conversationId: string
  title: string
  displayTitle: string
  preview: string
  kind: 'main' | 'subagent' | 'swarm'
  agentName?: string
  parentConversationId?: string
  workspacePath?: string
  workspaceName: string
  createdAt?: string
  lastModifiedTime: string
  stepCount: number
  turnCount: number
  exactTurnCount: number
  estimatedTurnCount: number
  primaryModelId: string
  primaryModelDisplayName: string
  models: string[]
  dataSource: string
  exact: ExactTokenTotals
  estimated: EstimatedTokenTotals
}

export interface ConversationDetail {
  summary: ConversationSummaryItem
  modelBreakdown: ConversationModelSlice[]
  turns: TurnUsageRecord[]
}

export interface UsageTrendBucket {
  key: string
  label: string
  turnCount: number
  conversationCount: number
  exact: ExactTokenTotals
  estimatedTotalTokens: number
}

export interface ModelUsageSummary {
  modelId: string
  modelDisplayName: string
  apiProvider: string
  turnCount: number
  conversationCount: number
  exact: ExactTokenTotals
  estimatedTotalTokens: number
}

export interface WorkspaceUsageSummary {
  workspaceName: string
  workspacePath?: string
  conversationCount: number
  turnCount: number
  exact: ExactTokenTotals
  estimatedTotalTokens: number
}

export interface TokenUsageQuery {
  preset: 'today' | '7d' | '30d' | 'month' | 'all'
  granularity: 'day' | 'week' | 'month'
  kind: string
  model: string
  workspace: string
  search: string
  sort: 'lastModifiedTime' | 'totalTokens' | 'turnCount'
  order: 'desc' | 'asc'
  page: number
  pageSize: number
}

export interface TokenUsageOverview {
  exact: ExactTokenTotals
  estimated: EstimatedTokenTotals
  conversationCount: number
  turnCount: number
  exactTurnCount: number
  trend: UsageTrendBucket[]
  models: ModelUsageSummary[]
  workspaces: WorkspaceUsageSummary[]
  availableModels: string[]
  availableWorkspaces: string[]
  scanDurationMs: number
  scannedFiles: number
  cachedFiles: number
  skippedErrors: number
  lastSyncedAt: string
}

export interface PaginatedConversations {
  items: ConversationSummaryItem[]
  total: number
  page: number
  pageSize: number
  totalPages: number
}

export interface TokenUsageSettings {
  customDataDir: string
  effectiveDataDir: string
  defaultDataDir: string
  dataDirExists: boolean
}

export interface TokenUsageBundle {
  overview: TokenUsageOverview
  conversations: PaginatedConversations
  settings: TokenUsageSettings
}

export type WakeupRepeatMode = 'daily' | 'weekly' | 'interval' | 'crontab' | 'quota_reset'

export interface WakeupSchedule {
  repeatMode: WakeupRepeatMode
  dailyTimes?: string[]
  weeklyDays?: number[]
  weeklyTimes?: string[]
  intervalHours?: number
  intervalStartTime?: string
  intervalEndTime?: string
  crontab?: string
  wakeOnReset: boolean
  selectedModels: string[]
  selectedAccounts: string[]
  customPrompt?: string
  maxOutputTokens?: number
}

export interface WakeupTask {
  id: string
  name: string
  enabled: boolean
  lastRunAt?: number
  createdAt: number
  schedule: WakeupSchedule
}

export interface WakeupState {
  enabled: boolean
  tasks: WakeupTask[]
}

export interface WakeupHistoryItem {
  id: string
  timestamp: number
  taskId?: string
  taskName?: string
  triggerType: 'manual' | 'scheduled'
  accountId: string
  accountEmail: string
  model: string
  prompt: string
  success: boolean
  reply?: string
  promptTokens?: number
  completionTokens?: number
  totalTokens?: number
  durationMs: number
  errorMessage?: string
  validationUrl?: string
}

export interface WakeupTestRequest {
  scopeId?: string
  accountId: string
  model: string
  prompt?: string
  maxOutputTokens?: number
}

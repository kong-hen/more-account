// Wails v2 Go 绑定调用桥接层，统一处理原生运行环境与错误格式化。
import type {
  Account,
  AccountDetailsUpdate,
  AccountGroup,
  ActiveDetectionResult,
  AppSettings,
  BatchImportResult,
  BootstrapState,
  ClientInstallInfo,
  ConversationDetail,
  OAuthLoginSession,
  RefreshStats,
  SwitchHistoryItem,
  SwitchOutcome,
  SwitchRequest,
  TokenUsageBundle,
  TokenUsageQuery,
  TotpCodeResult,
  UpdateCheckResult,
  WakeupHistoryItem,
  WakeupState,
  WakeupTestRequest,
} from '@/types/models'

interface WailsAppBridge {
  GetBootstrapState(): Promise<BootstrapState>
  GetAppVersion(): Promise<string>
  CheckForUpdates(repoOverride: string): Promise<UpdateCheckResult>
  SaveSettings(settings: AppSettings): Promise<AppSettings>
  TestProxy(rawProxyURL: string): Promise<number>
  DetectInstalledClients(): Promise<ClientInstallInfo>
  OpenExternalURL(rawURL: string): Promise<void>
  OpenDataFolder(): Promise<void>
  PickDirectory(title: string): Promise<string>
  QuitApp(): Promise<void>

  ListAccounts(): Promise<Account[]>
  GetCurrentAccountID(): Promise<string>
  ImportRefreshTokens(rawInput: string, groupId: string): Promise<BatchImportResult>
  ImportFromLocal(groupId: string): Promise<Account>
  ExportAccounts(accountIds: string[], includeSecrets: boolean): Promise<string>
  SaveExportToFile(accountIds: string[], includeSecrets: boolean): Promise<string>
  UpdateAccountDetails(accountId: string, update: AccountDetailsUpdate): Promise<Account>
  UpdateAccountGroup(accountIds: string[], groupId: string): Promise<void>
  DeleteAccounts(accountIds: string[]): Promise<void>
  ReorderAccounts(orderedIds: string[]): Promise<void>
  GenerateTOTP(secret: string): Promise<TotpCodeResult>

  StartOAuthLogin(groupId: string): Promise<OAuthLoginSession>
  PrepareOAuthLogin(groupId: string): Promise<OAuthLoginSession>
  GetActiveOAuthSession(): Promise<OAuthLoginSession | null>
  SetOAuthTargetGroup(groupId: string): Promise<void>
  SubmitOAuthCallbackURL(rawURL: string): Promise<void>
  CancelOAuthLogin(): Promise<void>

  RefreshAccountQuota(accountId: string): Promise<Account>
  RefreshAllQuotas(): Promise<RefreshStats>
  TriggerAutoRefreshIfDue(): Promise<void>
  DetectActiveAccount(): Promise<ActiveDetectionResult>
  TriggerActiveDetect(): Promise<void>

  SwitchAccount(req: SwitchRequest): Promise<SwitchOutcome>
  ListSwitchHistory(): Promise<SwitchHistoryItem[]>
  ClearSwitchHistory(): Promise<void>

  ListGroups(): Promise<AccountGroup[]>
  SaveGroup(group: AccountGroup): Promise<AccountGroup>
  DeleteGroup(groupId: string): Promise<void>

  QueryTokenUsage(query: TokenUsageQuery): Promise<TokenUsageBundle>
  SyncTokenUsage(rebuild: boolean, query: TokenUsageQuery): Promise<TokenUsageBundle>
  GetConversationDetail(conversationId: string): Promise<ConversationDetail>
  SaveTokenUsageCSV(query: TokenUsageQuery): Promise<string>

  GetWakeupState(): Promise<WakeupState>
  SaveWakeupState(state: WakeupState): Promise<void>
  RunWakeupTest(req: WakeupTestRequest): Promise<WakeupHistoryItem>
  RunWakeupTaskNow(taskId: string): Promise<WakeupHistoryItem[]>
  CancelWakeupTest(scopeId: string): Promise<void>
  ListWakeupHistory(): Promise<WakeupHistoryItem[]>
  ClearWakeupHistory(): Promise<void>
}

interface WailsRuntimeBridge {
  EventsOn(eventName: string, callback: (...data: unknown[]) => void): () => void
}

declare global {
  interface Window {
    go?: {
      main?: {
        App?: WailsAppBridge
      }
    }
    runtime?: WailsRuntimeBridge
  }
}

function getBridge(): WailsAppBridge {
  const bridge = window.go?.main?.App
  if (!bridge) {
    throw new Error('未检测到 Wails 原生运行时环境，请通过桌面客户端打开。')
  }
  return bridge
}

export function isWailsRuntime(): boolean {
  return Boolean(window.go?.main?.App)
}

export function onWailsEvent(eventName: string, handler: (...args: unknown[]) => void): () => void {
  const rt = window.runtime
  if (!rt?.EventsOn) {
    return () => {}
  }
  return rt.EventsOn(eventName, handler)
}

export function extractErrorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  if (typeof err === 'string') return err
  return String(err ?? '未知错误')
}

export const api = {
  getBootstrapState: () => getBridge().GetBootstrapState(),
  getAppVersion: () => getBridge().GetAppVersion(),
  checkForUpdates: (repoOverride = '') => getBridge().CheckForUpdates(repoOverride),
  saveSettings: (settings: AppSettings) => getBridge().SaveSettings(settings),
  testProxy: (rawProxyURL: string) => getBridge().TestProxy(rawProxyURL),
  detectInstalledClients: () => getBridge().DetectInstalledClients(),
  openExternalURL: (rawURL: string) => getBridge().OpenExternalURL(rawURL),
  openDataFolder: () => getBridge().OpenDataFolder(),
  pickDirectory: (title: string) => getBridge().PickDirectory(title),
  quitApp: () => getBridge().QuitApp(),

  listAccounts: () => getBridge().ListAccounts(),
  getCurrentAccountID: () => getBridge().GetCurrentAccountID(),
  importRefreshTokens: (rawInput: string, groupId: string) =>
    getBridge().ImportRefreshTokens(rawInput, groupId),
  importFromLocal: (groupId: string) => getBridge().ImportFromLocal(groupId),
  exportAccounts: (accountIds: string[], includeSecrets: boolean) =>
    getBridge().ExportAccounts(accountIds, includeSecrets),
  saveExportToFile: (accountIds: string[], includeSecrets: boolean) =>
    getBridge().SaveExportToFile(accountIds, includeSecrets),
  updateAccountDetails: (accountId: string, update: AccountDetailsUpdate) =>
    getBridge().UpdateAccountDetails(accountId, update),
  updateAccountGroup: (accountIds: string[], groupId: string) =>
    getBridge().UpdateAccountGroup(accountIds, groupId),
  deleteAccounts: (accountIds: string[]) => getBridge().DeleteAccounts(accountIds),
  reorderAccounts: (orderedIds: string[]) => getBridge().ReorderAccounts(orderedIds),
  generateTOTP: (secret: string) => getBridge().GenerateTOTP(secret),

  startOAuthLogin: (groupId: string) => getBridge().StartOAuthLogin(groupId),
  prepareOAuthLogin: (groupId: string) => getBridge().PrepareOAuthLogin(groupId),
  getActiveOAuthSession: () => getBridge().GetActiveOAuthSession(),
  setOAuthTargetGroup: (groupId: string) => getBridge().SetOAuthTargetGroup(groupId),
  submitOAuthCallbackURL: (rawURL: string) => getBridge().SubmitOAuthCallbackURL(rawURL),
  cancelOAuthLogin: () => getBridge().CancelOAuthLogin(),

  refreshAccountQuota: (accountId: string) => getBridge().RefreshAccountQuota(accountId),
  refreshAllQuotas: () => getBridge().RefreshAllQuotas(),
  triggerAutoRefreshIfDue: () => getBridge().TriggerAutoRefreshIfDue(),
  detectActiveAccount: () => getBridge().DetectActiveAccount(),
  triggerActiveDetect: () => getBridge().TriggerActiveDetect(),

  switchAccount: (req: SwitchRequest) => getBridge().SwitchAccount(req),
  listSwitchHistory: () => getBridge().ListSwitchHistory(),
  clearSwitchHistory: () => getBridge().ClearSwitchHistory(),

  listGroups: () => getBridge().ListGroups(),
  saveGroup: (group: AccountGroup) => getBridge().SaveGroup(group),
  deleteGroup: (groupId: string) => getBridge().DeleteGroup(groupId),

  queryTokenUsage: (query: TokenUsageQuery) => getBridge().QueryTokenUsage(query),
  syncTokenUsage: (rebuild: boolean, query: TokenUsageQuery) =>
    getBridge().SyncTokenUsage(rebuild, query),
  getConversationDetail: (conversationId: string) =>
    getBridge().GetConversationDetail(conversationId),
  saveTokenUsageCSV: (query: TokenUsageQuery) => getBridge().SaveTokenUsageCSV(query),

  getWakeupState: () => getBridge().GetWakeupState(),
  saveWakeupState: (state: WakeupState) => getBridge().SaveWakeupState(state),
  runWakeupTest: (req: WakeupTestRequest) => getBridge().RunWakeupTest(req),
  runWakeupTaskNow: (taskId: string) => getBridge().RunWakeupTaskNow(taskId),
  cancelWakeupTest: (scopeId: string) => getBridge().CancelWakeupTest(scopeId),
  listWakeupHistory: () => getBridge().ListWakeupHistory(),
  clearWakeupHistory: () => getBridge().ClearWakeupHistory(),
}

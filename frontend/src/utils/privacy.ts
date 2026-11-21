// 隐私脱敏显示与系统剪贴板复制工具。

export function maskEmail(email: string, enabled: boolean): string {
  if (!enabled) return email
  const trimmed = (email ?? '').trim()
  const atIdx = trimmed.indexOf('@')
  if (atIdx <= 0) {
    return trimmed.length <= 2 ? '***' : `${trimmed.slice(0, 2)}***`
  }
  const local = trimmed.slice(0, atIdx)
  const domain = trimmed.slice(atIdx + 1)
  const visibleLocal = local.length <= 2 ? local[0] + '*' : `${local.slice(0, 2)}***`
  return `${visibleLocal}@${domain}`
}

// 令牌默认脱敏显示：保留前后少量识别字符，隐藏核心密钥段。
export function maskToken(token: string): string {
  const trimmed = (token ?? '').trim()
  if (!trimmed) return ''
  if (trimmed.length <= 12) return '••••••••••••'
  return `${trimmed.slice(0, 6)}••••••••••••${trimmed.slice(-4)}`
}

export async function copyToClipboard(text: string): Promise<boolean> {
  if (!text) return false
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    try {
      const textarea = document.createElement('textarea')
      textarea.value = text
      textarea.style.position = 'fixed'
      textarea.style.opacity = '0'
      document.body.appendChild(textarea)
      textarea.select()
      const ok = document.execCommand('copy')
      document.body.removeChild(textarea)
      return ok
    } catch {
      return false
    }
  }
}

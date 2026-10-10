// 多源路由工具：源由 URL 前缀推导，天然可分享、可前进后退。
//
//   /bika/...           → bika 源（显式前缀，规范形式）
//   /jm/...             → jm 源（显式前缀）
//   无前缀（/、/search…）→ jm 源（历史兼容别名，老链接/书签不失效）
//
// 设计要点：不引入全局可变状态，全部由当前 URL 推导，
// 因此不存在「切换源时的竞态」，任何异步回调里调用也是正确的。

export type SourceKind = 'jm' | 'bika'

export interface SourceMeta {
  kind: SourceKind
  /** 顶栏切换器展示名 */
  label: string
  /** 规范化前缀（jm 同时接受无前缀与 /jm） */
  prefix: string
}

export const SOURCES: SourceMeta[] = [
  { kind: 'jm', label: 'JM', prefix: '/jm' },
  { kind: 'bika', label: '哔咔', prefix: '/bika' },
]

const BIKA_PREFIX = '/bika'
const JM_PREFIX = '/jm'

function hasPrefix(pathname: string, prefix: string): boolean {
  return pathname === prefix || pathname.startsWith(prefix + '/')
}

/** 从路径推导当前源。 */
export function sourceFromPath(pathname: string = window.location.pathname): SourceKind {
  return hasPrefix(pathname, BIKA_PREFIX) ? 'bika' : 'jm'
}

/** 当前 URL 使用的源前缀（无前缀时返回空串，保持历史链接形态不变）。 */
export function sourcePrefix(pathname: string = window.location.pathname): string {
  if (hasPrefix(pathname, BIKA_PREFIX)) return BIKA_PREFIX
  if (hasPrefix(pathname, JM_PREFIX)) return JM_PREFIX
  return ''
}

/** appLink：给站内绝对路径补上当前源的 URL 前缀，保证跨页跳转不丢源。 */
export function appLink(path: string): string {
  if (!path.startsWith('/')) return path
  const prefix = sourcePrefix()
  if (!prefix) return path
  if (hasPrefix(path, prefix)) return path
  return prefix + path
}

/** 把某源下的「逻辑路径」映射为真实 URL（用于切换器跨源跳转）。 */
export function sourcePath(kind: SourceKind, logicalPath: string): string {
  const prefix = kind === 'bika' ? BIKA_PREFIX : '' // JM 保持无前缀的兼容形态
  if (logicalPath === '/' || logicalPath === '') return prefix || '/'
  return prefix + logicalPath
}

/**
 * apiSourcePath：把页面里的 `/api/v2/jm/...` 跟随当前源改写为 `/api/v2/<当前源>/...`。
 * 这样各页面无需逐个改造 API 路径，也保证 bika 页面不会误打 jm 接口。
 */
export function apiSourcePath(path: string): string {
  const marker = '/api/v2/jm/'
  if (!path.startsWith(marker)) return path
  const source = sourceFromPath()
  if (source === 'jm') return path
  // 注意：marker 自带尾斜杠，替换后必须补回，否则会拼成 /api/v2/bikacategories。
  return '/api/v2/' + source + '/' + path.slice(marker.length)
}

/** 去掉源前缀后的「逻辑路径」，用于跨源映射当前页面。 */
export function logicalPathOf(pathname: string = window.location.pathname): string {
  const prefix = sourcePrefix(pathname)
  if (!prefix) return pathname
  const rest = pathname.slice(prefix.length)
  return rest === '' ? '/' : rest
}

/** bika 源不支持的逻辑路径（没有小说，也没有 JM 专有的 legacy「最新」流与社区板块）。 */
export function bikaSupportsLogical(logical: string): boolean {
  if (logical === '/latest' || logical === '/novels') return false
  if (logical === '/community' || logical === '/recommend') return false
  if (logical.startsWith('/novel/') || logical.startsWith('/novel_reader/')) return false
  return true
}

/** 当前源是否支持「评论点赞」（哔咔上游不提供该能力）。 */
export function commentLikeSupported(): boolean {
  return sourceFromPath() !== 'bika'
}

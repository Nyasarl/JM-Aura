import { api } from './api'
import type { RecommendComment, RecommendCommentList, Recommendation, RecommendationList, WorkKind } from './types'

/** 帖子正文上限，与服务端 store.RecommendMaxBody 保持一致。 */
export const POST_BODY_MAX = 1000
/** 单条评论上限，与服务端 store.CommunityMaxComment 保持一致。 */
export const COMMENT_BODY_MAX = 500
/** 一条帖子最多引用几张外链图片，与服务端 store.CommunityMaxImages 保持一致。 */
export const POST_IMAGES_MAX = 9

/** 社区（原「阅读笔记」）接口。
 *
 * 性能约定：点赞数、评论数、我是否点过赞、能否删除、评论作者昵称全部由服务端算好返回，
 * 前端只做渲染和分页，不在客户端聚合或轮询。 */
export const communityApi = {
  list: (page: number, pageSize: number) =>
    api.get<RecommendationList>(`/api/recommend${api.qs({ page, page_size: pageSize })}`),
  create: (input: {
    body: string
    comic_id?: string
    kind?: WorkKind
    source?: string
    images?: string[]
  }) => api.post<{ status: string; post: Recommendation }>('/api/recommend', input),
  remove: (id: string) => api.del<{ status: string }>(`/api/recommend/${encodeURIComponent(id)}`),
  like: (id: string) =>
    api.post<{ liked: boolean; like_count: number }>(`/api/recommend/${encodeURIComponent(id)}/like`),
  comments: (id: string, page: number, pageSize: number) =>
    api.get<RecommendCommentList>(
      `/api/recommend/${encodeURIComponent(id)}/comments${api.qs({ page, page_size: pageSize })}`,
    ),
  comment: (id: string, body: string) =>
    api.post<RecommendComment>(`/api/recommend/${encodeURIComponent(id)}/comments`, { body }),
  removeComment: (commentId: string) =>
    api.del<{ status: string }>(`/api/recommend/comments/${encodeURIComponent(commentId)}`),
}

/** 从多行文本里解析图片链接：按行拆分、去空白、去重，只保留 http/https，最多 9 条。
 *  服务端仍会再校验一次，这里只是让用户即时看到「识别到几张」。 */
export function parseImageLinks(text: string): string[] {
  const out: string[] = []
  const seen = new Set<string>()
  for (const raw of text.split(/[\n\s]+/)) {
    const s = raw.trim()
    if (!s || seen.has(s)) continue
    if (!/^https?:\/\//i.test(s)) continue
    seen.add(s)
    out.push(s)
    if (out.length >= POST_IMAGES_MAX) break
  }
  return out
}

/** 相对时间：一条帖子只渲染一次，字符串在渲染时算好即可，无需定时器。 */
export function timeAgo(ms: number): string {
  if (!ms) return ''
  const diff = Date.now() - ms
  if (diff < 60_000) return '刚刚'
  const min = Math.floor(diff / 60_000)
  if (min < 60) return `${min} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 小时前`
  const day = Math.floor(hour / 24)
  if (day < 30) return `${day} 天前`
  return new Date(ms).toLocaleDateString()
}